#!/usr/bin/env python3
"""Mock do Infodesk (Infominas Desk) para testar o fluxo ponta a ponta SEM Infodesk real.

O mock finge ser o Infodesk nos dois sentidos da integração — sem tocar no Go:

  Você (UI)  ──"message.received" (HMAC)──►  gateway Go  /webhook
                                                 │ (fila → cérebro Python)
    UI  ◄── POST .../conversations/:id/messages ─┘  (Go "responde no Infodesk" = aqui)

Ou seja:
  1. Quando o "cliente" escreve na UI (ou via `POST /mock/cliente/mensagem`), o
     mock cria/reusa a conversa do telefone, grava a mensagem com `sender`
     `CLIENT` e faz POST no webhook do gateway com o payload de
     docs/infodesk/05 assinado (`X-Infominas-Signature: sha256=<hmac>`).
  2. O gateway processa e, para responder/transferir/abrir ticket, chama a REST
     do Infodesk. Basta apontar `INFODESK_BASE_URL` para `http://localhost:9000/api`:
     o mock implementa `/api/integration/v1/{desks,users,departments,tickets,
     conversations}` com estado em memória e erros realistas (401, 403, 404,
     429, 4xx de validação) no formato `{"statusCode", "message"}` (docs/infodesk/06).
  3. O botão "Encerrar conversa" faz o papel do atendente: a conversa vira
     `FINISHED` e `conversation.finished` é emitido ao gateway.

Rodar:
    python3 mock_infodesk.py                        # porta 9000 (default)
    MOCK_PORT=9000 MOCK_API_KEY=ik_test_mock python3 mock_infodesk.py

Tudo que o PDF do Infodesk NÃO documenta está implementado como HIPÓTESE de
trabalho (itens de docs/infodesk/07-pontos-ambiguos.md citados no código) e
é configurável por variável de ambiente — ver README.md desta pasta.

Stdlib apenas — nenhum `pip install`.
"""

from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import re
import sys
import threading
import urllib.error
import urllib.request
import uuid
from datetime import UTC, datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import parse_qs, urlparse

AQUI = Path(__file__).resolve().parent

ESCOPOS_TODOS = ("tickets:read", "tickets:write", "conversations:read", "conversations:write")

# Prefixos aceitos. O gateway usa INFODESK_BASE_URL=http://localhost:9000/api e
# monta /integration/v1/... em cima (docs/infodesk/README); o segundo prefixo é
# só conveniência para curl.
PREFIXOS = ("/api/integration/v1", "/integration/v1")

# Teto do corpo de uma requisição (o Infodesk real não documenta; evita estourar memória no mock).
CORPO_MAX_BYTES = 1_000_000

# Timeout de entrega do webhook (docs/infodesk/05: 8 s, best-effort, sem reentrega).
WEBHOOK_TIMEOUT_S = 8.0


# ---------------------------------------------------------------------------
# Configuração (env com fallback; sobrescrevível por flags da CLI)
# ---------------------------------------------------------------------------


def _env_bool(nome: str, default: bool = False) -> bool:
    return os.environ.get(nome, "1" if default else "0").strip().lower() in (
        "1",
        "true",
        "sim",
        "yes",
    )


def config_do_ambiente() -> dict[str, Any]:
    """Lê a configuração do ambiente. Cada chave é documentada no README."""
    escopos = os.environ.get("MOCK_ESCOPOS", ",".join(ESCOPOS_TODOS))
    return {
        "port": int(os.environ.get("MOCK_PORT", "9000")),
        # Os endpoints /mock/* não têm autenticação: só loopback por padrão (MOCK_HOST=0.0.0.0 para docker).
        "host": os.environ.get("MOCK_HOST", "127.0.0.1"),
        "api_key": os.environ.get("MOCK_API_KEY", "ik_test_mock"),
        "escopos": sorted({e.strip() for e in escopos.split(",") if e.strip()}),
        "a_cada_429": int(os.environ.get("MOCK_429_A_CADA", "0")),
        "number_como_string": _env_bool("MOCK_NUMBER_COMO_STRING"),
        "gateway_webhook_url": os.environ.get(
            "GATEWAY_WEBHOOK_URL", "http://localhost:8080/webhook"
        ),
        "webhook_secret": os.environ.get("MOCK_WEBHOOK_SECRET", "segredo-mock"),
        "org_id": os.environ.get("MOCK_ORG_ID", "44444444-4444-4444-8444-444444444401"),
        "dados": os.environ.get("MOCK_DADOS", str(AQUI / "dados.json")),
        # Hipótese C5: /finish dispara conversation.finished também para o parceiro.
        "finish_emite_webhook": _env_bool("MOCK_FINISH_EMITE_WEBHOOK", True),
        # Hipótese A3: /desks, /users, /departments não exigem escopo (só chave válida).
        "referencia_exige_escopo": _env_bool("MOCK_REFERENCIA_EXIGE_ESCOPO", False),
        "primeiro_number": int(os.environ.get("MOCK_PRIMEIRO_NUMBER", "1001")),
    }


# ---------------------------------------------------------------------------
# Utilidades
# ---------------------------------------------------------------------------


def agora_iso() -> str:
    """ISO-8601 UTC com milissegundos e `Z`, como no exemplo de docs/infodesk/05."""
    return datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


def novo_id() -> str:
    return str(uuid.uuid4())


def mascarar_telefone(telefone: str) -> str:
    """PII mascarada no log (CLAUDE.md regra 5): só os 4 últimos dígitos."""
    digitos = re.sub(r"\D", "", telefone or "")
    return "***" + digitos[-4:] if len(digitos) >= 4 else "***"


def assinar(secret: str, raw: bytes) -> str:
    """`sha256=<hmac-hex>` do corpo bruto (docs/infodesk/05)."""
    return "sha256=" + hmac.new(secret.encode("utf-8"), raw, hashlib.sha256).hexdigest()


class ErroApi(Exception):
    """Erro no formato do Infodesk: `{"statusCode": N, "message": "..."}` (docs/infodesk/06)."""

    def __init__(self, status: int, message: str, headers: dict[str, str] | None = None):
        super().__init__(message)
        self.status = status
        self.message = message
        self.headers = headers or {}


# ---------------------------------------------------------------------------
# Estado em memória
# ---------------------------------------------------------------------------


class MockInfodesk:
    """Estado + regras do Infodesk falso. Uma instância por servidor (testável sem rede)."""

    def __init__(self, config: dict[str, Any]):
        self.config = config
        self.lock = threading.RLock()
        self.referencia = self._carregar_referencia(config["dados"])
        self.tickets: dict[int, dict[str, Any]] = {}
        self.conversas: dict[str, dict[str, Any]] = {}
        self.entregas: list[dict[str, Any]] = []  # histórico de webhooks emitidos
        self.proximo_number = int(config["primeiro_number"])
        self.contador_requisicoes = 0
        self.log = print

    # --- referência ---------------------------------------------------------

    @staticmethod
    def _carregar_referencia(caminho: str) -> dict[str, list[dict[str, Any]]]:
        with open(caminho, encoding="utf-8") as f:
            dados = json.load(f)
        ref = {k: list(dados.get(k, [])) for k in ("desks", "users", "departments")}
        for k, itens in ref.items():
            if not itens:
                raise SystemExit(f"dados.json: lista '{k}' vazia ou ausente em {caminho}")
        return ref

    def _acha(self, colecao: str, id_: Any) -> dict[str, Any] | None:
        return next((x for x in self.referencia[colecao] if x["id"] == id_), None)

    def reset(self) -> None:
        with self.lock:
            self.tickets.clear()
            self.conversas.clear()
            self.entregas.clear()
            self.proximo_number = int(self.config["primeiro_number"])
            self.contador_requisicoes = 0

    # --- autenticação / escopo / rate limit -----------------------------------

    def autenticar(self, headers: Any) -> None:
        """401 se a chave não vier em `Authorization: Bearer` nem em `X-Api-Key` (docs/infodesk/01)."""
        chave = ""
        auth = headers.get("Authorization") or ""
        if auth.lower().startswith("bearer "):
            chave = auth[7:].strip()
        if not chave:
            chave = (headers.get("X-Api-Key") or "").strip()
        esperada = str(self.config["api_key"]).encode("utf-8")
        if not chave or not hmac.compare_digest(chave.encode("utf-8"), esperada):
            raise ErroApi(401, "Chave de API ausente, inválida ou revogada.")

    def exigir_escopo(self, escopo: str | None) -> None:
        if escopo and escopo not in self.config["escopos"]:
            # Formato literal do PDF (docs/infodesk/01).
            raise ErroApi(403, f"Escopo insuficiente. Necessário: {escopo}.")

    def contar_para_429(self) -> None:
        """A cada N requisições autenticadas devolve 429 (MOCK_429_A_CADA; 0 = nunca)."""
        n = int(self.config["a_cada_429"])
        if n <= 0:
            return
        with self.lock:
            self.contador_requisicoes += 1
            estourou = self.contador_requisicoes % n == 0
        if estourou:
            # Retry-After não está documentado (docs/infodesk/06) — hipótese: 1 s.
            raise ErroApi(429, "Limite de requisições excedido.", {"Retry-After": "1"})

    # --- tickets ---------------------------------------------------------------

    def _number_externo(self, number: int) -> int | str:
        # T1: tipo de `number` desconhecido — MOCK_NUMBER_COMO_STRING=1 devolve string.
        return str(number) if self.config["number_como_string"] else number

    def _ticket_externo(self, t: dict[str, Any]) -> dict[str, Any]:
        saida = dict(t)
        saida["number"] = self._number_externo(t["number"])
        return saida

    def criar_ticket(self, corpo: dict[str, Any]) -> dict[str, Any]:
        """`POST /tickets` (docs/infodesk/03): só `deskId` e `title` são obrigatórios."""
        desk_id = corpo.get("deskId")
        title = corpo.get("title")
        if not isinstance(desk_id, str) or not desk_id.strip():
            raise ErroApi(400, "Campo obrigatório ausente: deskId.")
        if not isinstance(title, str) or not title.strip():
            raise ErroApi(400, "Campo obrigatório ausente: title.")
        if self._acha("desks", desk_id) is None:
            # Hipótese (06 só diz "4xx de validação"): 400 e não 404 para mesa inexistente.
            raise ErroApi(400, f"deskId inválido: mesa não encontrada ({desk_id}).")
        priority_id = corpo.get("priorityId")
        for campo in (
            "description",
            "channel",
            "clientId",
            "requesterId",
            "contactPhone",
            "priorityId",
        ):
            valor = corpo.get(campo)
            if valor is not None and not isinstance(valor, str):
                raise ErroApi(400, f"Campo inválido: {campo} deve ser string.")
        with self.lock:
            number = self.proximo_number
            self.proximo_number += 1
            agora = agora_iso()
            # Schema da resposta além de `number` é HIPÓTESE (07, item T1).
            ticket = {
                "id": novo_id(),
                "number": number,
                "title": title.strip(),
                "description": corpo.get("description") or "",
                "status": "OPEN",
                "deskId": desk_id,
                "channel": corpo.get("channel") or "WEB",
                "clientId": corpo.get("clientId"),
                "requesterId": corpo.get("requesterId"),
                "contactPhone": corpo.get("contactPhone"),
                "priorityId": priority_id,
                "createdAt": agora,
                "updatedAt": agora,
                "replies": [],
            }
            self.tickets[number] = ticket
            externo = self._ticket_externo(ticket)
        self.log(f"[mock] ticket #{number} criado na mesa {self._acha('desks', desk_id)['name']!r}")
        return externo

    def _ticket(self, number_str: str) -> dict[str, Any]:
        try:
            number = int(number_str)
        except ValueError:
            raise ErroApi(404, "Ticket não encontrado.") from None
        with self.lock:
            t = self.tickets.get(number)
        if t is None:
            raise ErroApi(404, "Ticket não encontrado.")
        return t

    def consultar_ticket(self, number_str: str) -> dict[str, Any]:
        with self.lock:
            return self._ticket_externo(self._ticket(number_str))

    def responder_ticket(self, number_str: str, corpo: dict[str, Any]) -> dict[str, Any]:
        """`POST /tickets/:number/reply` {body}. Só registra no ticket (T7: destino da resposta é desconhecido)."""
        body = corpo.get("body")
        if not isinstance(body, str) or not body.strip():
            raise ErroApi(400, "Campo obrigatório ausente: body.")
        with self.lock:
            t = self._ticket(number_str)
            reply = {"id": novo_id(), "body": body, "createdAt": agora_iso()}
            t["replies"].append(reply)
            t["updatedAt"] = reply["createdAt"]
        return reply

    # --- conversas -------------------------------------------------------------

    def _conversa_externa(self, c: dict[str, Any], com_mensagens: bool = False) -> dict[str, Any]:
        # Schema de item de GET /conversations é HIPÓTESE (07, item C1).
        saida = {
            "id": c["id"],
            "status": c["status"],
            "channel": c["channel"],
            "contact": dict(c["contact"]),
            "departmentId": c["departmentId"],
            "assignedUserId": c["assignedUserId"],
            "lastMessageAt": c["lastMessageAt"],
            "createdAt": c["createdAt"],
            "finishedAt": c["finishedAt"],
        }
        if com_mensagens:
            # Campos internos do mock (prefixo "_") não saem pela API.
            saida["messages"] = [
                {k: v for k, v in m.items() if not k.startswith("_")} for m in c["messages"]
            ]
        return saida

    def listar_conversas(self, tab: str | None, department_id: str | None) -> list[dict[str, Any]]:
        """`GET /conversations?tab=&departmentId=`. Semântica de `tab` é HIPÓTESE (07, item C1)."""
        if tab is not None and tab not in ("entrada", "meus", "atendimento", "encerrados"):
            raise ErroApi(
                400, "Parâmetro inválido: tab deve ser entrada, meus, atendimento ou encerrados."
            )
        with self.lock:
            itens = list(self.conversas.values())
        if tab == "entrada":  # abertas sem atendente
            itens = [c for c in itens if c["status"] == "OPEN" and not c["assignedUserId"]]
        elif tab in ("meus", "atendimento"):  # abertas com atendente (o mock não distingue "meus")
            itens = [c for c in itens if c["status"] == "OPEN" and c["assignedUserId"]]
        elif tab == "encerrados":
            itens = [c for c in itens if c["status"] == "FINISHED"]
        if department_id:
            itens = [c for c in itens if c["departmentId"] == department_id]
        itens.sort(key=lambda c: c["lastMessageAt"] or c["createdAt"], reverse=True)
        return [self._conversa_externa(c) for c in itens]

    def _conversa(self, conv_id: str, aberta: bool = False) -> dict[str, Any]:
        c = self.conversas.get(conv_id)
        if c is None:
            raise ErroApi(404, "Conversa não encontrada.")
        if aberta and c["status"] != "OPEN":
            # Hipótese (docs/infodesk/06, política nossa): operação em conversa encerrada = 404.
            raise ErroApi(404, "Conversa não encontrada ou encerrada.")
        return c

    def ler_conversa(self, conv_id: str) -> dict[str, Any]:
        with self.lock:
            return self._conversa_externa(self._conversa(conv_id), com_mensagens=True)

    def _gravar_mensagem(self, c: dict[str, Any], body: str, sender: str) -> dict[str, Any]:
        msg = {"id": novo_id(), "body": body, "sender": sender, "createdAt": agora_iso()}
        c["messages"].append(msg)
        c["lastMessageAt"] = msg["createdAt"]
        return msg

    def enviar_mensagem(self, conv_id: str, corpo: dict[str, Any]) -> dict[str, Any]:
        """`POST /conversations/:id/messages` {body}: registra como sender BOT (C2: valor não documentado)."""
        body = corpo.get("body")
        if not isinstance(body, str) or not body.strip():
            raise ErroApi(400, "Campo obrigatório ausente: body.")
        with self.lock:
            c = self._conversa(conv_id, aberta=True)
            msg = self._gravar_mensagem(c, body, "BOT")
        self.log(f"[mock] bot → conversa {conv_id[:8]}…: {len(body)} chars")
        return dict(msg)

    def atribuir(self, conv_id: str, corpo: dict[str, Any]) -> dict[str, Any]:
        """`POST /conversations/:id/assign` {userId}. `null` desatribui (hipótese; o PDF não documenta)."""
        if "userId" not in corpo:
            raise ErroApi(400, "Campo obrigatório ausente: userId.")
        user_id = corpo["userId"]
        if user_id is not None and self._acha("users", user_id) is None:
            raise ErroApi(400, f"userId inválido: atendente não encontrado ({user_id}).")
        with self.lock:
            c = self._conversa(conv_id, aberta=True)
            c["assignedUserId"] = user_id
            self._evento_sistema(c, "atendente → " + (self._nome("users", user_id) or "nenhum"))
            return self._conversa_externa(c)

    def transferir_departamento(self, conv_id: str, corpo: dict[str, Any]) -> dict[str, Any]:
        """`POST /conversations/:id/department` {departmentId} — `null` remove (literal do PDF, docs/infodesk/04)."""
        if "departmentId" not in corpo:
            raise ErroApi(400, "Campo obrigatório ausente: departmentId.")
        dep_id = corpo["departmentId"]
        if dep_id is not None and self._acha("departments", dep_id) is None:
            raise ErroApi(400, f"departmentId inválido: departamento não encontrado ({dep_id}).")
        with self.lock:
            c = self._conversa(conv_id, aberta=True)
            c["departmentId"] = dep_id
            self._evento_sistema(
                c, "departamento → " + (self._nome("departments", dep_id) or "nenhum")
            )
            return self._conversa_externa(c)

    def encerrar(self, conv_id: str) -> dict[str, Any]:
        """`POST /conversations/:id/finish` (sem corpo). Idempotente. Emite conversation.finished (hipótese C5)."""
        with self.lock:
            c = self._conversa(conv_id)
            ja_encerrada = c["status"] == "FINISHED"
            if not ja_encerrada:
                c["status"] = "FINISHED"
                c["finishedAt"] = agora_iso()
                self._evento_sistema(c, "conversa encerrada")
            externo = self._conversa_externa(c)
        if not ja_encerrada and self.config["finish_emite_webhook"]:
            self.emitir_webhook("conversation.finished", c)
        return externo

    def _nome(self, colecao: str, id_: Any) -> str | None:
        item = self._acha(colecao, id_) if id_ else None
        return item.get("name") or item.get("username") if item else None

    def _evento_sistema(self, c: dict[str, Any], texto: str) -> None:
        """Nota só para a UI (não aparece em GET /conversations/:id)."""
        c["eventos"].append({"texto": texto, "createdAt": agora_iso()})

    # --- lado "cliente" (UI / E2E) --------------------------------------------

    def _conversa_do_telefone(self, telefone: str, nome: str) -> tuple[dict[str, Any], bool]:
        """Reusa a conversa aberta do telefone ou cria uma nova (C5: após finish, nova conversa = novo id)."""
        for c in self.conversas.values():
            if c["contact"]["phone"] == telefone and c["status"] == "OPEN":
                if nome:
                    c["contact"]["name"] = nome
                return c, False
        agora = agora_iso()
        c = {
            "id": novo_id(),
            "status": "OPEN",
            "channel": "WHATSAPP",
            "contact": {"name": nome or "Cliente", "phone": telefone},
            "departmentId": None,
            "assignedUserId": None,
            "lastMessageAt": None,
            "createdAt": agora,
            "finishedAt": None,
            "messages": [],
            "eventos": [],
        }
        self.conversas[c["id"]] = c
        return c, True

    def mensagem_do_cliente(
        self, telefone: str, nome: str, texto: str, audio: bool = False
    ) -> dict[str, Any]:
        """Grava a mensagem do cliente e emite os webhooks (started na 1ª vez, depois message.received)."""
        telefone = (telefone or "").strip()
        if not telefone:
            raise ErroApi(400, "telefone é obrigatório.")
        if not audio and not (texto or "").strip():
            raise ErroApi(400, "texto é obrigatório (ou use áudio).")
        with self.lock:
            c, nova = self._conversa_do_telefone(telefone, (nome or "").strip())
            # W3: áudio/mídia = message.received com body vazio (hipótese).
            msg = self._gravar_mensagem(c, "" if audio else texto.strip(), "CLIENT")
            if audio:
                msg["_mock_tipo"] = "audio"
        self.log(
            f"[mock] cliente {mascarar_telefone(telefone)} → conversa {c['id'][:8]}… ({'áudio' if audio else 'texto'})"
        )
        entregas = []
        if nova:
            entregas.append(self.emitir_webhook("conversation.started", c))
        entregas.append(self.emitir_webhook("message.received", c, msg))
        return {
            "conversationId": c["id"],
            "messageId": msg["id"],
            "novaConversa": nova,
            "entregas": entregas,
        }

    def encerrar_pelo_telefone(self, telefone: str) -> dict[str, Any]:
        with self.lock:
            c = next(
                (
                    c
                    for c in self.conversas.values()
                    if c["contact"]["phone"] == telefone and c["status"] == "OPEN"
                ),
                None,
            )
        if c is None:
            raise ErroApi(404, "Nenhuma conversa aberta para esse telefone.")
        return self.encerrar(c["id"])

    def estado_para_ui(self, telefone: str | None) -> dict[str, Any]:
        with self.lock:
            conversa = None
            if telefone:
                abertas = [c for c in self.conversas.values() if c["contact"]["phone"] == telefone]
                abertas.sort(key=lambda c: (c["status"] == "OPEN", c["createdAt"]))
                if abertas:
                    c = abertas[-1]
                    conversa = self._conversa_externa(c, com_mensagens=True)
                    conversa["messages"] = [
                        {**m, "tipo": m.get("_mock_tipo", "texto")} for m in c["messages"]
                    ]
                    for m in conversa["messages"]:
                        m.pop("_mock_tipo", None)
                    conversa["eventos"] = list(c["eventos"])
                    conversa["departmentName"] = self._nome("departments", c["departmentId"])
                    conversa["assignedUserName"] = self._nome("users", c["assignedUserId"])
            tickets = []
            for t in sorted(self.tickets.values(), key=lambda t: t["number"]):
                desk = self._acha("desks", t["deskId"])
                tickets.append(
                    {
                        "number": self._number_externo(t["number"]),
                        "title": t["title"],
                        "deskName": desk["name"] if desk else t["deskId"],
                        "status": t["status"],
                        "contactPhone": t.get("contactPhone"),
                        "replies": len(t["replies"]),
                        "createdAt": t["createdAt"],
                    }
                )
            return {
                "conversa": conversa,
                "tickets": tickets,
                "entregas": list(self.entregas[-20:]),
                "config": {
                    "gatewayWebhookUrl": self.config["gateway_webhook_url"],
                    "escopos": list(self.config["escopos"]),
                    "aCada429": self.config["a_cada_429"],
                    "numberComoString": self.config["number_como_string"],
                },
            }

    # --- webhooks --------------------------------------------------------------

    def montar_payload(
        self, evento: str, c: dict[str, Any], msg: dict[str, Any] | None
    ) -> dict[str, Any]:
        """Payload exato de docs/infodesk/05 para message.received; started/finished seguem a hipótese W1."""
        data: dict[str, Any] = {
            "conversationId": c["id"],
            "channel": c["channel"],
            "contact": dict(c["contact"]),
        }
        if evento == "message.received" and msg is not None:
            data["message"] = {
                "id": msg["id"],
                "body": msg["body"],
                "sender": msg["sender"],
                "createdAt": msg["createdAt"],
            }
        return {
            "id": novo_id(),
            "event": evento,
            "createdAt": agora_iso(),
            "organizationId": self.config["org_id"],
            "data": data,
        }

    def emitir_webhook(
        self, evento: str, c: dict[str, Any], msg: dict[str, Any] | None = None
    ) -> dict[str, Any]:
        """POST assinado no gateway. Síncrono, timeout 8 s, SEM reentrega: falha vai para o log e para a UI."""
        payload = self.montar_payload(evento, c, msg)
        raw = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        url = self.config["gateway_webhook_url"]
        req = urllib.request.Request(url, data=raw, method="POST")  # noqa: S310 — URL vem da config do dev
        req.add_header("Content-Type", "application/json")
        req.add_header("X-Infominas-Event", evento)
        req.add_header("X-Infominas-Signature", assinar(self.config["webhook_secret"], raw))
        registro = {
            "id": payload["id"],
            "event": evento,
            "conversationId": c["id"],
            "at": payload["createdAt"],
        }
        try:
            with urllib.request.urlopen(req, timeout=WEBHOOK_TIMEOUT_S) as resp:  # noqa: S310 — URL vem da config do dev
                registro.update(ok=True, status=resp.status, erro=None)
        except urllib.error.HTTPError as exc:
            with exc:  # fecha o corpo da resposta de erro
                registro.update(ok=False, status=exc.code, erro=f"gateway respondeu {exc.code}")
        except (urllib.error.URLError, OSError, TimeoutError) as exc:
            motivo = getattr(exc, "reason", exc)
            registro.update(
                ok=False, status=None, erro=f"sem resposta do gateway em {url}: {motivo}"
            )
        with self.lock:
            self.entregas.append(registro)
            if not registro["ok"]:
                self._evento_sistema(c, f"webhook {evento} falhou: {registro['erro']}")
        self.log(
            f"[mock] webhook {evento} → {url}: "
            + ("ok" if registro["ok"] else f"FALHA ({registro['erro']})")
        )
        return registro


# ---------------------------------------------------------------------------
# Roteamento HTTP
# ---------------------------------------------------------------------------

# (método, regex sobre o caminho sem prefixo, escopo exigido, handler)
ROTAS: list[tuple[str, re.Pattern[str], str | None, str]] = [
    ("GET", re.compile(r"^/desks$"), None, "desks"),
    ("GET", re.compile(r"^/users$"), None, "users"),
    ("GET", re.compile(r"^/departments$"), None, "departments"),
    ("POST", re.compile(r"^/tickets$"), "tickets:write", "criar_ticket"),
    ("GET", re.compile(r"^/tickets/([^/]+)$"), "tickets:read", "consultar_ticket"),
    ("POST", re.compile(r"^/tickets/([^/]+)/reply$"), "tickets:write", "responder_ticket"),
    ("GET", re.compile(r"^/conversations$"), "conversations:read", "listar_conversas"),
    ("GET", re.compile(r"^/conversations/([^/]+)$"), "conversations:read", "ler_conversa"),
    (
        "POST",
        re.compile(r"^/conversations/([^/]+)/messages$"),
        "conversations:write",
        "enviar_mensagem",
    ),
    ("POST", re.compile(r"^/conversations/([^/]+)/assign$"), "conversations:write", "atribuir"),
    (
        "POST",
        re.compile(r"^/conversations/([^/]+)/department$"),
        "conversations:write",
        "transferir_departamento",
    ),
    ("POST", re.compile(r"^/conversations/([^/]+)/finish$"), "conversations:write", "encerrar"),
]
ROTAS_REFERENCIA = {"desks", "users", "departments"}


class ServidorMock(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, endereco: tuple[str, int], mock: MockInfodesk):
        super().__init__(endereco, Handler)
        self.mock = mock


class Handler(BaseHTTPRequestHandler):
    server_version = "MockInfodesk/1.0"
    protocol_version = "HTTP/1.1"
    server: ServidorMock  # type: ignore[assignment]

    # --- helpers de resposta ------------------------------------------------

    def _json(self, obj: Any, status: int = 200, headers: dict[str, str] | None = None) -> None:
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        if self.close_connection:  # corpo não drenado: avisa o cliente que vai fechar
            self.send_header("Connection", "close")
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def _html(self, html: str) -> None:
        body = html.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _erro(self, status: int, message: str, headers: dict[str, str] | None = None) -> None:
        self._json({"statusCode": status, "message": message}, status, headers)

    def _drenar_corpo(self) -> None:
        """Lê o corpo inteiro ANTES de responder: com keep-alive (cliente Go) um corpo
        não lido ficaria no socket e corromperia a próxima requisição da conexão."""
        # Transfer-Encoding: chunked não é suportado (o cliente Go manda Content-Length).
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            length = -1
        if length < 0 or length > CORPO_MAX_BYTES:
            # Corpo não lido: fecha a conexão depois da resposta para os bytes
            # que sobraram não virarem a "próxima requisição" do keep-alive.
            self._corpo_bruto = b""
            self._corpo_invalido = True
            self.close_connection = True
            return
        self._corpo_invalido = False
        self._corpo_bruto = self.rfile.read(length) if length > 0 else b""

    def _ler_json(self) -> dict[str, Any]:
        if self._corpo_invalido:
            raise ErroApi(
                400,
                f"Corpo inválido: Content-Length ausente, inválido ou acima de {CORPO_MAX_BYTES} bytes.",
            )
        raw = self._corpo_bruto
        if not raw.strip():
            return {}
        try:
            dados = json.loads(raw.decode("utf-8"))
        except (json.JSONDecodeError, UnicodeDecodeError):
            raise ErroApi(400, "Corpo inválido: esperado JSON.") from None
        if not isinstance(dados, dict):
            raise ErroApi(400, "Corpo inválido: esperado objeto JSON.")
        return dados

    def log_message(self, fmt: str, *args: Any) -> None:
        # Uma linha por requisição no stdout. Só o caminho, SEM query string
        # (/mock/estado?telefone=... tem PII — CLAUDE.md regra 5). Em /mock/*
        # sequências longas de dígitos são mascaradas por garantia; na API os
        # caminhos só têm UUIDs e numbers de ticket, que ficam legíveis no log.
        caminho = urlparse(self.path).path
        if caminho.startswith("/mock/"):
            caminho = re.sub(r"\d{5,}", lambda m: mascarar_telefone(m.group(0)), caminho)
        self.server.mock.log(
            f"[mock] {self.command} {caminho} → {args[1] if len(args) > 1 else '?'}"
        )

    # --- despacho -----------------------------------------------------------

    def do_GET(self) -> None:
        self._despachar("GET")

    def do_POST(self) -> None:
        self._despachar("POST")

    def _despachar(self, metodo: str) -> None:
        parsed = urlparse(self.path)
        path = parsed.path.rstrip("/") or "/"
        query = {k: v[0] for k, v in parse_qs(parsed.query).items()}
        mock = self.server.mock
        self._drenar_corpo()
        try:
            if path == "/" and metodo == "GET":
                self._html(INDEX_HTML)
                return
            if path.startswith("/mock/"):
                self._rota_mock(metodo, path, query)
                return
            for prefixo in PREFIXOS:
                if path.startswith(prefixo + "/"):
                    self._rota_api(metodo, path[len(prefixo) :], query)
                    return
            raise ErroApi(404, "Recurso não encontrado.")
        except ErroApi as exc:
            self._erro(exc.status, exc.message, exc.headers)
        except Exception as exc:
            # Só o tipo: a mensagem da exceção pode ecoar trechos do corpo (PII).
            mock.log(f"[mock] ERRO interno em {metodo} {path}: {type(exc).__name__}")
            self._erro(500, "Erro interno do mock.")

    def _rota_api(self, metodo: str, sub: str, query: dict[str, str]) -> None:
        mock = self.server.mock
        mock.autenticar(self.headers)
        mock.contar_para_429()
        for m, rx, escopo, nome in ROTAS:
            match = rx.match(sub)
            if not match:
                continue
            if m != metodo:
                # Hipótese: o PDF não documenta 405 (o Infodesk real pode devolver 404).
                raise ErroApi(405, f"Método não permitido: {metodo}.")
            if nome in ROTAS_REFERENCIA:
                # Hipótese A3 (opt-in): /desks com tickets:read; /users e /departments com conversations:read.
                if mock.config["referencia_exige_escopo"]:
                    mock.exigir_escopo("tickets:read" if nome == "desks" else "conversations:read")
                self._json(mock.referencia[nome])
                return
            mock.exigir_escopo(escopo)
            args = match.groups()
            # Códigos/corpos de sucesso são HIPÓTESE (03/04: "resposta não documentada"):
            # 201 + recurso criado em POST que cria (ticket, reply, message); 200 + conversa nos demais.
            if nome == "criar_ticket":
                self._json(mock.criar_ticket(self._ler_json()), 201)
            elif nome == "consultar_ticket":
                self._json(mock.consultar_ticket(args[0]))
            elif nome == "responder_ticket":
                self._json(mock.responder_ticket(args[0], self._ler_json()), 201)
            elif nome == "listar_conversas":
                self._json(mock.listar_conversas(query.get("tab"), query.get("departmentId")))
            elif nome == "ler_conversa":
                self._json(mock.ler_conversa(args[0]))
            elif nome == "enviar_mensagem":
                self._json(mock.enviar_mensagem(args[0], self._ler_json()), 201)
            elif nome == "atribuir":
                self._json(mock.atribuir(args[0], self._ler_json()))
            elif nome == "transferir_departamento":
                self._json(mock.transferir_departamento(args[0], self._ler_json()))
            elif nome == "encerrar":
                self._ler_json()  # corpo ignorado (não documentado)
                self._json(mock.encerrar(args[0]))
            return
        raise ErroApi(404, "Recurso não encontrado.")

    def _rota_mock(self, metodo: str, path: str, query: dict[str, str]) -> None:
        """Endpoints de controle (só do mock, sem autenticação): UI e E2E usam."""
        mock = self.server.mock
        if metodo == "GET" and path == "/mock/estado":
            self._json(mock.estado_para_ui((query.get("telefone") or "").strip() or None))
        elif metodo == "GET" and path == "/mock/config":
            self._json(
                {k: v for k, v in mock.config.items() if k not in ("api_key", "webhook_secret")}
            )
        elif metodo == "POST" and path == "/mock/cliente/mensagem":
            b = self._ler_json()
            self._json(
                mock.mensagem_do_cliente(
                    b.get("telefone", ""), b.get("nome", ""), b.get("texto", "")
                ),
                201,
            )
        elif metodo == "POST" and path == "/mock/cliente/audio":
            b = self._ler_json()
            self._json(
                mock.mensagem_do_cliente(b.get("telefone", ""), b.get("nome", ""), "", audio=True),
                201,
            )
        elif metodo == "POST" and path == "/mock/cliente/encerrar":
            b = self._ler_json()
            self._json(mock.encerrar_pelo_telefone((b.get("telefone") or "").strip()))
        elif metodo == "POST" and path == "/mock/reset":
            mock.reset()
            self._json({"ok": True})
        else:
            raise ErroApi(404, "Recurso não encontrado.")


# ---------------------------------------------------------------------------
# UI (HTML + JS vanilla, sem build) — embutida para rodar com 1 arquivo
# ---------------------------------------------------------------------------

INDEX_HTML = r"""<!doctype html>
<html lang="pt-br">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Mock Infodesk — Sofia</title>
<style>
  :root { --bg:#0b141a; --panel:#111b21; --bot:#202c33; --cli:#005c4b; --txt:#e9edef; --mut:#8696a0; --ok:#25d366; --err:#f15c6d; }
  * { box-sizing:border-box; }
  body { margin:0; font-family:system-ui,Segoe UI,Roboto,sans-serif; background:var(--bg); color:var(--txt); height:100vh; display:flex; flex-direction:column; }
  header { background:var(--panel); padding:10px 16px; display:flex; gap:10px; align-items:center; flex-wrap:wrap; border-bottom:1px solid #0008; }
  header h1 { font-size:15px; margin:0 12px 0 0; font-weight:600; }
  input { background:#0e1a21; border:1px solid #2a3942; color:var(--txt); padding:7px 10px; border-radius:8px; font-size:13px; }
  button { background:var(--cli); color:#fff; border:0; padding:7px 12px; border-radius:8px; cursor:pointer; font-size:13px; }
  button.sec { background:#2a3942; }
  button:disabled { opacity:.5; cursor:default; }
  .pill { font-size:12px; color:var(--mut); }
  main { flex:1; display:flex; min-height:0; }
  #log { flex:1; overflow-y:auto; padding:18px; display:flex; flex-direction:column; gap:8px; }
  .row { display:flex; }
  .row.CLIENT { justify-content:flex-end; }
  .row.BOT { justify-content:flex-start; }
  .row.sys { justify-content:center; }
  .bubble { max-width:72%; padding:8px 12px; border-radius:10px; font-size:14px; line-height:1.4; white-space:pre-wrap; word-break:break-word; }
  .CLIENT .bubble { background:var(--cli); }
  .BOT .bubble { background:var(--bot); }
  .sys .bubble { background:transparent; color:var(--mut); font-size:12px; font-style:italic; text-align:center; max-width:90%; }
  .bubble small { display:block; color:#cfd8dc99; font-size:10px; margin-top:4px; }
  aside { width:320px; background:var(--panel); border-left:1px solid #0008; padding:14px; overflow-y:auto; font-size:13px; }
  aside h2 { font-size:12px; text-transform:uppercase; letter-spacing:.05em; color:var(--mut); margin:14px 0 6px; }
  aside h2:first-child { margin-top:0; }
  aside dl { margin:0; display:grid; grid-template-columns:auto 1fr; gap:2px 8px; }
  aside dt { color:var(--mut); }
  aside dd { margin:0; word-break:break-all; }
  .ticket { background:#0e1a21; border-radius:8px; padding:6px 8px; margin-bottom:6px; }
  .ticket b { color:var(--ok); }
  .entrega { font-size:12px; padding:2px 0; }
  .entrega.ok::before { content:"✓ "; color:var(--ok); }
  .entrega.falha::before { content:"✗ "; color:var(--err); }
  .entrega .erro { color:var(--err); display:block; padding-left:14px; }
  footer { background:var(--panel); padding:12px 16px; display:flex; gap:10px; border-top:1px solid #0008; }
  footer input { flex:1; background:#2a3942; border:0; padding:11px 14px; border-radius:10px; font-size:14px; }
  .hint { color:var(--mut); font-size:12px; padding:8px 18px 0; }
  .vazio { color:var(--mut); font-style:italic; }
</style>
</head>
<body>
  <header>
    <h1>📱 Mock Infodesk</h1>
    <input id="telefone" placeholder="telefone E.164 (ex.: +5531990000001)" size="26" value="+5531990000001">
    <input id="nome" placeholder="nome do cliente" size="16" value="Maria">
    <button id="encerrar" class="sec" title="faz o papel do atendente: encerra a conversa e emite conversation.finished">Encerrar conversa</button>
    <span class="pill" id="info">—</span>
  </header>
  <div class="hint" id="hint">Escreva como o <b>cliente</b> no WhatsApp: a primeira mensagem cria a conversa (<code>conversation.started</code> + <code>message.received</code> assinados para o gateway). As respostas que o gateway enviar por <code>POST /conversations/:id/messages</code> aparecem como bolhas do bot. <b>🎤 Áudio</b> envia <code>message.received</code> com <code>body</code> vazio.</div>
  <main>
    <div id="log"></div>
    <aside>
      <h2>Conversa</h2>
      <dl id="conv"><dt>status</dt><dd class="vazio">nenhuma</dd></dl>
      <h2>Tickets criados</h2>
      <div id="tickets"><span class="vazio">nenhum</span></div>
      <h2>Webhooks emitidos</h2>
      <div id="entregas"><span class="vazio">nenhum</span></div>
      <h2>Config</h2>
      <dl id="cfg"></dl>
    </aside>
  </main>
  <footer>
    <input id="msg" placeholder="Digite a mensagem do cliente..." autocomplete="off">
    <button id="send">Enviar</button>
    <button id="audio" class="sec" title="message.received com body vazio (hipótese W3)">🎤 Áudio</button>
  </footer>

<script>
const $ = (id) => document.getElementById(id);
const log = $("log");
let vistos = new Set();
let convAtual = null;

function telefone() { return $("telefone").value.trim(); }
function nome() { return $("nome").value.trim(); }

function bubble(cls, text, meta) {
  const row = document.createElement("div");
  row.className = "row " + cls;
  const b = document.createElement("div");
  b.className = "bubble";
  b.textContent = text;
  if (meta) { const s = document.createElement("small"); s.textContent = meta; b.appendChild(s); }
  row.appendChild(b);
  log.appendChild(row);
  log.scrollTop = log.scrollHeight;
}

function renderConv(c) {
  const dl = $("conv");
  if (!c) { dl.innerHTML = '<dt>status</dt><dd class="vazio">nenhuma</dd>'; $("info").textContent = "—"; return; }
  const linhas = [
    ["id", c.id], ["status", c.status], ["canal", c.channel],
    ["departamento", c.departmentName || (c.departmentId ? c.departmentId : "—")],
    ["atendente", c.assignedUserName || (c.assignedUserId ? c.assignedUserId : "—")],
    ["última msg", c.lastMessageAt || "—"],
  ];
  dl.innerHTML = linhas.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join("");
  $("info").textContent = `conversa ${c.id.slice(0, 8)}… · ${c.status}`;
}

function renderTickets(ts) {
  const el = $("tickets");
  if (!ts.length) { el.innerHTML = '<span class="vazio">nenhum</span>'; return; }
  el.innerHTML = ts.map(t =>
    `<div class="ticket"><b>#${t.number}</b> ${esc(t.title)}<br><span class="pill">mesa: ${esc(t.deskName)} · ${t.status}${t.contactPhone ? " · " + esc(t.contactPhone) : ""}${t.replies ? " · " + t.replies + " reply" : ""}</span></div>`
  ).join("");
}

function renderEntregas(es) {
  const el = $("entregas");
  if (!es.length) { el.innerHTML = '<span class="vazio">nenhum</span>'; return; }
  el.innerHTML = es.slice().reverse().map(e =>
    `<div class="entrega ${e.ok ? "ok" : "falha"}">${e.event} <span class="pill">${e.at.slice(11, 19)}${e.status ? " · " + e.status : ""}</span>${e.ok ? "" : `<span class="erro">${esc(e.erro || "")}</span>`}</div>`
  ).join("");
}

function esc(s) { return String(s ?? "").replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c])); }

async function poll() {
  const tel = telefone();
  if (!tel) return;
  try {
    const r = await fetch(`/mock/estado?telefone=${encodeURIComponent(tel)}`);
    const d = await r.json();
    const c = d.conversa;
    if (!c || (convAtual && convAtual !== c.id)) { log.innerHTML = ""; vistos = new Set(); }
    convAtual = c ? c.id : null;
    if (c) {
      const itens = [
        ...c.messages.map(m => ({ k: "m:" + m.id, t: m.createdAt, m })),
        ...c.eventos.map((e, i) => ({ k: "e:" + i + e.createdAt, t: e.createdAt, e })),
      ].sort((a, b) => a.t.localeCompare(b.t));
      for (const it of itens) {
        if (vistos.has(it.k)) continue;
        vistos.add(it.k);
        if (it.m) bubble(it.m.sender === "CLIENT" ? "CLIENT" : "BOT", it.m.tipo === "audio" ? "🎤 (áudio — body vazio)" : it.m.body, `${it.m.sender} · ${it.m.createdAt.slice(11, 19)}`);
        else bubble("sys", "📌 " + it.e.texto);
      }
    }
    renderConv(c);
    renderTickets(d.tickets);
    renderEntregas(d.entregas);
    $("cfg").innerHTML = `<dt>webhook</dt><dd>${esc(d.config.gatewayWebhookUrl)}</dd><dt>escopos</dt><dd>${esc(d.config.escopos.join(", "))}</dd><dt>429 a cada</dt><dd>${d.config.aCada429 || "nunca"}</dd><dt>number</dt><dd>${d.config.numberComoString ? "string" : "número"}</dd>`;
  } catch (e) { /* mock fora do ar? ignora */ }
}

async function post(path, body) {
  const r = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const d = await r.json().catch(() => ({}));
  if (!r.ok) bubble("sys", "⚠️ " + (d.message || r.status));
  return d;
}

async function enviar() {
  const texto = $("msg").value.trim();
  if (!texto || !telefone()) { $(texto ? "telefone" : "msg").focus(); return; }
  $("msg").value = "";
  $("hint").style.display = "none";
  await post("/mock/cliente/mensagem", { telefone: telefone(), nome: nome(), texto });
  poll();
}

async function audio() {
  if (!telefone()) { $("telefone").focus(); return; }
  $("hint").style.display = "none";
  await post("/mock/cliente/audio", { telefone: telefone(), nome: nome() });
  poll();
}

async function encerrar() {
  if (!telefone()) return;
  await post("/mock/cliente/encerrar", { telefone: telefone() });
  poll();
}

$("send").addEventListener("click", enviar);
$("audio").addEventListener("click", audio);
$("encerrar").addEventListener("click", encerrar);
$("msg").addEventListener("keydown", (e) => { if (e.key === "Enter") enviar(); });
$("telefone").addEventListener("change", () => { log.innerHTML = ""; vistos = new Set(); convAtual = null; poll(); });
setInterval(poll, 1000);
poll();
</script>
</body>
</html>
"""


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def criar_servidor(
    config: dict[str, Any] | None = None, host: str | None = None, port: int | None = None
) -> ServidorMock:
    """Cria o servidor sem iniciá-lo (os testes usam port=0 para porta livre)."""
    cfg = dict(config_do_ambiente() if config is None else config)
    return ServidorMock(
        (host or cfg["host"], cfg["port"] if port is None else port), MockInfodesk(cfg)
    )


def main(argv: list[str] | None = None) -> None:
    # Windows com stdout redirecionado usa cp1252 e quebra nos caracteres do banner/log.
    for fluxo in (sys.stdout, sys.stderr):
        if hasattr(fluxo, "reconfigure"):
            fluxo.reconfigure(encoding="utf-8", errors="replace")
    cfg = config_do_ambiente()
    parser = argparse.ArgumentParser(description="Mock do Infodesk para teste E2E da Sofia.")
    parser.add_argument("--port", type=int, default=cfg["port"], help="porta HTTP (MOCK_PORT)")
    parser.add_argument("--host", default=cfg["host"], help="interface (MOCK_HOST)")
    parser.add_argument(
        "--gateway-webhook",
        default=cfg["gateway_webhook_url"],
        dest="gateway_webhook_url",
        help="URL do webhook do gateway (GATEWAY_WEBHOOK_URL)",
    )
    parser.add_argument(
        "--api-key", default=cfg["api_key"], dest="api_key", help="chave aceita (MOCK_API_KEY)"
    )
    parser.add_argument(
        "--dados", default=cfg["dados"], help="JSON de mesas/atendentes/departamentos (MOCK_DADOS)"
    )
    args = parser.parse_args(argv)
    cfg.update(
        port=args.port,
        host=args.host,
        gateway_webhook_url=args.gateway_webhook_url,
        api_key=args.api_key,
        dados=args.dados,
    )

    server = criar_servidor(cfg)
    print("┌─ Mock Infodesk ─────────────────────────────────────────────")
    print(f"│ UI:            http://localhost:{cfg['port']}")
    print(
        f"│ API:           http://localhost:{cfg['port']}/api/integration/v1  (INFODESK_BASE_URL=http://localhost:{cfg['port']}/api)"
    )
    print(
        f"│ Webhook→gw:    {cfg['gateway_webhook_url']}  (X-Infominas-Signature com MOCK_WEBHOOK_SECRET)"
    )
    print(
        f"│ Chave:         {cfg['api_key'][:8]}…  escopos: {', '.join(cfg['escopos']) or '(nenhum)'}"
    )
    print(
        f"│ 429 a cada:    {cfg['a_cada_429'] or 'nunca'}  ·  number como string: {cfg['number_como_string']}"
    )
    print("└─────────────────────────────────────────────────────────────")
    sys.stdout.flush()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nencerrando mock...")
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
