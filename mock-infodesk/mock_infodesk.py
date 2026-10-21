#!/usr/bin/env python3
"""Mock do Infodesk (Infominas Desk) para testar o fluxo ponta a ponta SEM Infodesk real.

Segue a API v1 de 2026-10-19 (docs/infodesk/01–09). O mock finge ser o Infodesk
nos dois sentidos da integração — sem tocar no Go:

  Você (UI)  ──"message.received" (HMAC)──►  gateway Go  /webhook
                                                 │ (fila → cérebro Python)
    UI  ◄── POST .../conversations/:id/messages ─┘  (Go "responde no Infodesk" = aqui)

Ou seja:
  1. Quando o "cliente" escreve na UI (ou via `POST /mock/cliente/mensagem`), o
     mock cria/reusa a conversa do telefone (`handledBy: EXTERNAL_BOT`), grava a
     mensagem com `sender` `CLIENT` e faz POST no webhook do gateway com o
     envelope de docs/infodesk/05 assinado: `X-Infominas-Timestamp` e
     `X-Infominas-Signature: sha256=HMAC(secret, "<ts>.<corpo bruto>")`, mais
     `X-Infominas-Delivery` (novo a cada entrega) e `id` do evento estável.
  2. O gateway processa e, para responder/transferir/abrir ticket, chama a REST
     do Infodesk. Basta apontar `INFODESK_BASE_URL` para `http://localhost:9000/api`:
     o mock implementa as rotas de docs/infodesk/01–08 usadas pela Sofia com
     estado em memória e erros no formato `{"statusCode","code","message"}` (06).
  3. Os botões "Encerrar conversa" e "Atendente assume" fazem o papel do
     atendente: `conversation.finished` / `conversation.assigned` (`handledBy:
     AGENT`, bot fora). "Reentregar último evento" repete o envelope com o
     mesmo `id` e outro `X-Infominas-Delivery` (dedup do gateway).

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
import time
import urllib.error
import urllib.request
import uuid
from datetime import UTC, datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, NamedTuple
from urllib.parse import parse_qs, urlparse

AQUI = Path(__file__).resolve().parent

# Escopos que a Sofia usa na v1 (docs/infodesk/01, "menor privilégio"). `*` na chave libera tudo.
ESCOPOS_TODOS = (
    "tickets:read",
    "tickets:write",
    "conversations:read",
    "conversations:write",
    "contacts:read",
    "events:read",
)

# Prefixos aceitos. O gateway usa INFODESK_BASE_URL=http://localhost:9000/api e
# monta /integration/v1/... em cima (docs/infodesk/README); o segundo prefixo é
# só conveniência para curl. # ver docs/infodesk/07 item A6 (base URL real é outra).
PREFIXOS = ("/api/integration/v1", "/integration/v1")

# Teto do corpo de uma requisição (o Infodesk real não documenta; evita estourar memória no mock).
CORPO_MAX_BYTES = 1_000_000

# Timeout de entrega do webhook (docs/infodesk/05: sucesso = 2xx em até 8 s).
WEBHOOK_TIMEOUT_S = 8.0

# Reentrega automática depois de falha (docs/infodesk/05: 30 s, 2 min, 10 min, 1 h, 6 h).
REENTREGA_ATRASOS_S = (30.0, 120.0, 600.0, 3600.0, 21600.0)

CANAIS_TICKET = ("WHATSAPP", "WEB", "EMAIL", "CHAT", "AGENT")  # docs/infodesk/07 item T2
PERIODOS = ("session", "today", "24h", "all")  # docs/infodesk/03
LIMITE_PADRAO = 50  # docs/infodesk/06
LIMITE_MAX = 100
LIMITE_EVENTOS_MAX = 500  # docs/infodesk/05 (exemplo com limit=500)


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


def _env_lista_float(nome: str, default: tuple[float, ...]) -> list[float]:
    bruto = os.environ.get(nome)
    if bruto is None:
        return list(default)
    return [float(x) for x in bruto.split(",") if x.strip()]


def config_do_ambiente() -> dict[str, Any]:
    """Lê a configuração do ambiente. Cada chave é documentada no README."""
    escopos = os.environ.get("MOCK_ESCOPOS", ",".join(ESCOPOS_TODOS))
    return {
        "port": int(os.environ.get("MOCK_PORT", "9000")),
        # Os endpoints /mock/* não têm autenticação: só loopback por padrão (MOCK_HOST=0.0.0.0 para docker).
        "host": os.environ.get("MOCK_HOST", "127.0.0.1"),
        "api_key": os.environ.get("MOCK_API_KEY", "ik_test_mock"),
        # Nome da chave = `senderName` das mensagens do bot (docs/infodesk/04 e 07 item C8).
        "nome_chave": os.environ.get("MOCK_NOME_CHAVE", "Bot de atendimento"),
        "escopos": sorted({e.strip() for e in escopos.split(",") if e.strip()}),
        "a_cada_429": int(os.environ.get("MOCK_429_A_CADA", "0")),
        "gateway_webhook_url": os.environ.get(
            "GATEWAY_WEBHOOK_URL", "http://localhost:8080/webhook"
        ),
        "webhook_secret": os.environ.get("MOCK_WEBHOOK_SECRET", "segredo-mock"),
        "org_id": os.environ.get("MOCK_ORG_ID", "44444444-4444-4444-8444-444444444401"),
        "dados": os.environ.get("MOCK_DADOS", str(AQUI / "dados.json")),
        # Sem reentrega AUTOMÁTICA após falha de entrega (E2E de dedup controla a reentrega
        # pelo botão/endpoint "reentregar último evento", que continua funcionando).
        "sem_reentrega": _env_bool("MOCK_SEM_REENTREGA"),
        "reentrega_atrasos_s": _env_lista_float("MOCK_REENTREGA_ATRASOS_S", REENTREGA_ATRASOS_S),
        # Hipótese C5: /finish dispara conversation.finished também para o parceiro.
        "finish_emite_webhook": _env_bool("MOCK_FINISH_EMITE_WEBHOOK", True),
        # Opt-in: referência (/desks, /users, ...) exigir escopo. O PDF v1 diz que NÃO exige
        # (docs/infodesk/07 item A3, respondido); a opção só serve para testar o tratamento de 403.
        "referencia_exige_escopo": _env_bool("MOCK_REFERENCIA_EXIGE_ESCOPO", False),
        "primeiro_number": int(os.environ.get("MOCK_PRIMEIRO_NUMBER", "1001")),
    }


# ---------------------------------------------------------------------------
# Utilidades
# ---------------------------------------------------------------------------


def agora_iso(dt: datetime | None = None) -> str:
    """ISO-8601 UTC com milissegundos e `Z`, como no exemplo de docs/infodesk/05."""
    return (dt or datetime.now(UTC)).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3] + "Z"


def novo_id() -> str:
    return str(uuid.uuid4())


def digitos(texto: str | None) -> str:
    return re.sub(r"\D", "", texto or "")


def mascarar_telefone(telefone: str) -> str:
    """PII mascarada no log (CLAUDE.md regra 5): só os 4 últimos dígitos."""
    d = digitos(telefone)
    return "***" + d[-4:] if len(d) >= 4 else "***"


def assinar(secret: str, timestamp: str, raw: bytes) -> str:
    """`sha256=<hmac-hex>` de `"<timestamp>.<corpo bruto>"` (docs/infodesk/05, seção 5.4)."""
    msg = timestamp.encode("utf-8") + b"." + raw
    return "sha256=" + hmac.new(secret.encode("utf-8"), msg, hashlib.sha256).hexdigest()


class ErroApi(Exception):
    """Erro no formato do Infodesk: `{"statusCode","code","message", ...extras}` (docs/infodesk/06)."""

    def __init__(
        self,
        status: int,
        code: str,
        message: str,
        extras: dict[str, Any] | None = None,
        headers: dict[str, str] | None = None,
    ):
        super().__init__(message)
        self.status = status
        self.code = code
        self.message = message
        self.extras = extras or {}
        self.headers = headers or {}

    def corpo(self) -> dict[str, Any]:
        return {
            "statusCode": self.status,
            "code": self.code,
            "message": self.message,
            **self.extras,
        }


def invalido(campo: str | None, message: str) -> ErroApi:
    """`400 VALIDATION_ERROR`. Formato do array `errors` NÃO é documentado (docs/infodesk/06)."""
    erros = [{"field": campo, "message": message}] if campo else []
    return ErroApi(400, "VALIDATION_ERROR", message, {"errors": erros})


def referencia_invalida(message: str) -> ErroApi:
    return ErroApi(400, "INVALID_REFERENCE", message)


def nao_encontrado(message: str) -> ErroApi:
    return ErroApi(404, "NOT_FOUND", message)


def exigir_str_ou_none(corpo: dict[str, Any], *campos: str) -> None:
    for campo in campos:
        valor = corpo.get(campo)
        if valor is not None and not isinstance(valor, str):
            raise invalido(campo, f"Campo inválido: {campo} deve ser string.")


def limite_da_query(query: dict[str, str], padrao: int, maximo: int) -> int:
    """`limit` fora de [1, max] é cortado no máximo (comportamento real não documentado, 06)."""
    bruto = query.get("limit")
    if bruto is None:
        return padrao
    try:
        n = int(bruto)
    except ValueError:
        raise invalido("limit", "Parâmetro inválido: limit deve ser inteiro.") from None
    if n < 1:
        raise invalido("limit", "Parâmetro inválido: limit deve ser >= 1.")
    return min(n, maximo)


def paginar(itens: list[Any], query: dict[str, str]) -> dict[str, Any]:
    """`{data, nextCursor}` (docs/infodesk/06). O cursor do mock é só o deslocamento (opaco para o cliente)."""
    limite = limite_da_query(query, LIMITE_PADRAO, LIMITE_MAX)
    bruto = query.get("cursor")
    try:
        inicio = 0 if bruto in (None, "") else int(bruto)  # type: ignore[arg-type]
    except ValueError:
        raise invalido("cursor", "Parâmetro inválido: cursor.") from None
    if inicio < 0:
        raise invalido("cursor", "Parâmetro inválido: cursor.")
    fim = inicio + limite
    return {"data": itens[inicio:fim], "nextCursor": str(fim) if fim < len(itens) else None}


# ---------------------------------------------------------------------------
# Estado em memória
# ---------------------------------------------------------------------------


class MockInfodesk:
    """Estado + regras do Infodesk falso. Uma instância por servidor (testável sem rede).

    Os métodos públicos que atendem rotas têm a assinatura `(args, query, corpo)`; os
    que mutam estado seguram `self.lock` e só fazem a entrega do webhook (rede) DEPOIS
    de soltá-lo.
    """

    def __init__(self, config: dict[str, Any]):
        self.config = config
        self.lock = threading.RLock()
        self.referencia = self._carregar_referencia(config["dados"])
        self.tickets: dict[int, dict[str, Any]] = {}
        self.conversas: dict[str, dict[str, Any]] = {}
        self.eventos: list[dict[str, Any]] = []  # feed de GET /events (envelopes)
        self.entregas: list[dict[str, Any]] = []  # cada tentativa de entrega de webhook
        self.timers: list[threading.Timer] = []
        # `seq` só cresce (nem o /mock/reset volta atrás): um gateway que guardou o maior
        # `seq` processado não pode ver o contador regredir (docs/infodesk/05).
        self.seq = 0
        self.proximo_number = int(config["primeiro_number"])
        self.contador_requisicoes = 0
        self.log = print

    # --- referência ---------------------------------------------------------

    @staticmethod
    def _carregar_referencia(caminho: str) -> dict[str, list[dict[str, Any]]]:
        with open(caminho, encoding="utf-8") as f:
            dados = json.load(f)
        ref = {
            k: list(dados.get(k, []))
            for k in ("desks", "users", "departments", "clients", "requesters")
        }
        for k in ("desks", "users", "departments"):
            if not ref[k]:
                raise SystemExit(f"dados.json: lista '{k}' vazia ou ausente em {caminho}")
        for desk in ref["desks"]:
            for sub in ("statuses", "stages", "priorities", "catalogItems"):
                if not isinstance(desk.get(sub), list):
                    raise SystemExit(f"dados.json: mesa {desk.get('name')!r} sem a lista '{sub}'")
            if not any(s.get("isOpen") for s in desk["statuses"]):
                raise SystemExit(f"dados.json: mesa {desk.get('name')!r} sem status aberto")
        return ref

    def _acha(self, colecao: str, id_: Any) -> dict[str, Any] | None:
        return next((x for x in self.referencia[colecao] if x["id"] == id_), None)

    def reset(self) -> None:
        with self.lock:
            for t in self.timers:
                t.cancel()
            self.timers.clear()
            self.tickets.clear()
            self.conversas.clear()
            self.eventos.clear()
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
            raise ErroApi(401, "UNAUTHORIZED", "Chave de API ausente, inválida ou revogada.")

    def exigir_escopo(self, escopo: str | None) -> None:
        if not escopo:
            return
        escopos = self.config["escopos"]
        if escopo not in escopos and "*" not in escopos:
            # Mensagem no formato literal do guia anterior (a `message` é só para humanos; o `code` decide).
            raise ErroApi(403, "FORBIDDEN", f"Escopo insuficiente. Necessário: {escopo}.")

    def contar_para_429(self) -> None:
        """A cada N requisições autenticadas devolve 429 (MOCK_429_A_CADA; 0 = nunca)."""
        n = int(self.config["a_cada_429"])
        if n <= 0:
            return
        with self.lock:
            self.contador_requisicoes += 1
            estourou = self.contador_requisicoes % n == 0
        if estourou:
            # ver docs/infodesk/07 item A4: Retry-After não está documentado — hipótese: 1 s.
            raise ErroApi(
                429,
                "RATE_LIMITED",
                "Limite de requisições excedido.",
                headers={"Retry-After": "1"},
            )

    # --- referência (rotas) ------------------------------------------------------

    def me(self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]) -> Any:
        """`GET /me` (docs/infodesk/01): quem é a chave e a `organizationId` dos webhooks."""
        return {
            "organizationId": self.config["org_id"],
            "keyId": str(uuid.uuid5(uuid.NAMESPACE_URL, "mock-infodesk/chave")),
            "name": self.config["nome_chave"],
            "scopes": list(self.config["escopos"]),
            "handlesConversations": True,
        }

    def listar_mesas(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        # ver docs/infodesk/07 item D3: array puro (o real pode paginar como {data, nextCursor}).
        return [{"id": d["id"], "name": d["name"]} for d in self.referencia["desks"]]

    def ler_mesa(self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]) -> Any:
        """`GET /desks/:id` (docs/infodesk/02): mesa com statuses, stages, priorities e catalogItems."""
        desk = self._acha("desks", args[0])
        if desk is None:
            raise nao_encontrado("Mesa não encontrada.")
        return json.loads(json.dumps(desk))

    def listar_catalogo(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`GET /catalog?deskId=`: itens do catálogo da mesa (campos além de id/name: 07 item D5)."""
        desk_id = query.get("deskId")
        if not desk_id:
            raise invalido("deskId", "Parâmetro obrigatório ausente: deskId.")
        desk = self._acha("desks", desk_id)
        if desk is None:
            raise nao_encontrado("Mesa não encontrada.")
        return [dict(i) for i in desk["catalogItems"]]

    def listar_usuarios(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        return [dict(u) for u in self.referencia["users"]]

    def listar_departamentos(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        return [dict(d) for d in self.referencia["departments"]]

    def lookup_contato(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`GET /contacts/lookup?phone=` (docs/infodesk/04): identifica o telefone."""
        bruto = (query.get("phone") or "").strip()
        alvo = digitos(bruto)
        if not alvo:
            raise invalido("phone", "Parâmetro obrigatório ausente: phone.")
        phone = "+" + alvo  # `+` sem escape na query vira espaço; só os dígitos importam
        requesters = []
        for r in self.referencia["requesters"]:
            if digitos(r.get("phone")) == alvo:
                requesters.append(self._solicitante_com_cliente(r))
        ids_solicitantes = {r["id"] for r in requesters}
        with self.lock:
            abertos = [
                self._ticket_externo(t)
                for t in sorted(self.tickets.values(), key=lambda t: t["number"])
                if self._ticket_aberto(t)
                and (
                    digitos(t.get("contactPhone")) == alvo
                    or t.get("requesterId") in ids_solicitantes
                )
            ]
            ativa = next(
                (
                    self._conversa_externa(c)
                    for c in self.conversas.values()
                    if digitos(c["contact"]["phone"]) == alvo and c["status"] != "FINISHED"
                ),
                None,
            )
        client_ids = sorted({r["clientId"] for r in requesters if r.get("clientId")})
        return {
            "phone": phone,
            "requesters": requesters,
            # ver docs/infodesk/07 item N2: formato de clientContacts não documentado.
            "clientContacts": [],
            "clientIds": client_ids,
            "openTickets": abertos,
            "conversation": ativa,
        }

    def _solicitante_com_cliente(self, r: dict[str, Any]) -> dict[str, Any]:
        saida = dict(r)
        cliente = self._acha("clients", r.get("clientId"))
        saida["client"] = {"id": cliente["id"], "name": cliente["name"]} if cliente else None
        return saida

    # --- tickets ---------------------------------------------------------------

    def _ticket_aberto(self, t: dict[str, Any]) -> bool:
        return bool(self._status_do_ticket(t)["isOpen"])

    def _status_do_ticket(self, t: dict[str, Any]) -> dict[str, Any]:
        desk = self._acha("desks", t["deskId"]) or {"statuses": []}
        return next(s for s in desk["statuses"] if s["id"] == t["statusId"])

    def _ticket_externo(self, t: dict[str, Any], com_mensagens: bool = False) -> dict[str, Any]:
        """Objeto `ticket` de docs/infodesk/03 (seção 9.1): `number` inteiro, `status` objeto."""
        desk = self._acha("desks", t["deskId"]) or {}
        status = self._status_do_ticket(t)
        etapa = next((e for e in desk.get("stages", []) if e["id"] == t["stageId"]), None)
        prioridade = next(
            (p for p in desk.get("priorities", []) if p["id"] == t["priorityId"]), None
        )
        cliente = self._acha("clients", t["clientId"]) if t["clientId"] else None
        solicitante = self._acha("requesters", t["requesterId"]) if t["requesterId"] else None
        saida: dict[str, Any] = {
            "id": t["id"],
            "number": t["number"],
            "title": t["title"],
            "description": t["description"],
            "channel": t["channel"],
            "ticketType": t["ticketType"],
            "impact": t["impact"],
            "urgency": t["urgency"],
            "priorityLevel": None,
            "desk": {"id": desk.get("id"), "name": desk.get("name")},
            "status": dict(status),
            "stage": {"id": etapa["id"], "name": etapa["name"]} if etapa else None,
            "priority": {"id": prioridade["id"], "name": prioridade["name"]}
            if prioridade
            else None,
            "catalogItemId": t["catalogItemId"],
            "client": {"id": cliente["id"], "name": cliente["name"]} if cliente else None,
            "requester": (
                {k: solicitante.get(k) for k in ("id", "name", "email", "phone")}
                if solicitante
                else None
            ),
            "resourceId": None,
            "responsible": None,
            "contactPhone": t["contactPhone"],
            "conversationId": t["conversationId"],
            "isClosed": bool(status["isClose"]),
            "closedAt": None,
            "isDraft": False,
            # ver docs/infodesk/03: prazos de SLA existem no objeto, mas o mock não calcula SLA.
            "serviceDueAt": None,
            "solutionDueAt": None,
            "firstRespondedAt": None,
            "customValues": None,
            "createdAt": t["createdAt"],
            "updatedAt": t["updatedAt"],
        }
        if com_mensagens:
            # ver docs/infodesk/07 item T13: formato de `messages` não documentado — histórico
            # público = respostas (/reply); notas internas ficam de fora.
            saida["messages"] = [dict(r) for r in t["replies"]]
        return saida

    def _abrir_ticket(
        self,
        corpo: dict[str, Any],
        conversa: dict[str, Any] | None = None,
        extra_descricao: str = "",
    ) -> dict[str, Any]:
        """Cria o ticket (compartilhado por `POST /tickets` e `POST /conversations/:id/ticket`).

        Segura o lock do chamador. Só `deskId` e `title` são obrigatórios aqui; quais campos a
        Infominas exige é desconhecido (docs/infodesk/07 item T10).
        """
        desk_id = corpo.get("deskId")
        title = corpo.get("title")
        if not isinstance(desk_id, str) or not desk_id.strip():
            raise invalido("deskId", "Campo obrigatório ausente: deskId.")
        if not isinstance(title, str) or not title.strip():
            raise invalido("title", "Campo obrigatório ausente: title.")
        exigir_str_ou_none(
            corpo,
            "description",
            "channel",
            "clientId",
            "requesterId",
            "contactPhone",
            "priorityId",
            "catalogItemId",
            "statusId",
            "stageId",
            "ticketType",
            "impact",
            "urgency",
        )
        canal = corpo.get("channel")
        if canal is not None and canal not in CANAIS_TICKET:
            raise invalido("channel", f"channel inválido: use um de {', '.join(CANAIS_TICKET)}.")
        desk = self._acha("desks", desk_id)
        if desk is None:
            raise referencia_invalida(f"deskId inválido: mesa não encontrada ({desk_id}).")
        for campo, lista in (
            ("priorityId", desk["priorities"]),
            ("catalogItemId", desk["catalogItems"]),
            ("statusId", desk["statuses"]),
            ("stageId", desk["stages"]),
        ):
            valor = corpo.get(campo)
            if valor is not None and not any(i["id"] == valor for i in lista):
                raise referencia_invalida(f"{campo} inválido: não existe na mesa {desk['name']!r}.")
        client_id = corpo.get("clientId")
        requester_id = corpo.get("requesterId")
        if client_id is not None and self._acha("clients", client_id) is None:
            raise referencia_invalida(f"clientId inválido: cliente não encontrado ({client_id}).")
        if requester_id is not None:
            solicitante = self._acha("requesters", requester_id)
            if solicitante is None:
                raise referencia_invalida(
                    f"requesterId inválido: solicitante não encontrado ({requester_id})."
                )
            client_id = client_id or solicitante.get("clientId")  # "o cliente vem junto" (03)
        status = next(s for s in desk["statuses"] if s["isOpen"])
        descricao = (corpo.get("description") or "") + extra_descricao
        contato = corpo.get("contactPhone")
        if conversa is not None:
            # ver docs/infodesk/07 item T11: hipótese — herda cliente/solicitante/telefone da conversa.
            client_id = client_id or conversa["clientId"]
            requester_id = requester_id or conversa["requesterId"]
            contato = contato or conversa["contact"]["phone"]
        number = self.proximo_number
        self.proximo_number += 1
        agora = agora_iso()
        ticket = {
            "id": novo_id(),
            "number": number,
            "title": title.strip(),
            "description": descricao,
            "channel": canal or ("WHATSAPP" if conversa is not None else "WEB"),
            "ticketType": corpo.get("ticketType"),
            "impact": corpo.get("impact"),
            "urgency": corpo.get("urgency"),
            "deskId": desk_id,
            "statusId": corpo.get("statusId") or status["id"],
            "stageId": corpo.get("stageId") or desk["stages"][0]["id"],
            "priorityId": corpo.get("priorityId"),
            "catalogItemId": corpo.get("catalogItemId"),
            "clientId": client_id,
            "requesterId": requester_id,
            "contactPhone": contato,
            "conversationId": conversa["id"] if conversa is not None else None,
            "createdAt": agora,
            "updatedAt": agora,
            "replies": [],
            "notes": [],
        }
        self.tickets[number] = ticket
        self.log(f"[mock] ticket #{number} criado na mesa {desk['name']!r}")
        return ticket

    def criar_ticket(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`POST /tickets` (docs/infodesk/03): `deskId` e `title` obrigatórios."""
        with self.lock:
            return self._ticket_externo(self._abrir_ticket(corpo))

    def _ticket(self, number_str: str) -> dict[str, Any]:
        try:
            number = int(number_str)
        except ValueError:
            raise nao_encontrado("Chamado não encontrado.") from None
        t = self.tickets.get(number)
        if t is None:
            raise nao_encontrado("Chamado não encontrado.")
        return t

    def consultar_ticket(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        with self.lock:
            return self._ticket_externo(self._ticket(args[0]), com_mensagens=True)

    def listar_tickets(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`GET /tickets?status=open|closed|all&deskId&clientId&requesterId&responsibleId&phone&updatedSince&limit&cursor`."""
        status = query.get("status", "open")  # padrão: abertos (docs/infodesk/03)
        if status not in ("open", "closed", "all"):
            raise invalido("status", "Parâmetro inválido: status deve ser open, closed ou all.")
        alvo = digitos(query.get("phone")) if query.get("phone") else ""
        with self.lock:
            itens = sorted(self.tickets.values(), key=lambda t: t["number"], reverse=True)
            if status == "open":
                itens = [t for t in itens if self._ticket_aberto(t)]
            elif status == "closed":
                itens = [t for t in itens if not self._ticket_aberto(t)]
            for campo, chave in (
                ("deskId", "deskId"),
                ("clientId", "clientId"),
                ("requesterId", "requesterId"),
            ):
                if query.get(campo):
                    itens = [t for t in itens if t[chave] == query[campo]]
            if query.get("responsibleId"):
                itens = []  # o mock não atribui responsável
            if alvo:
                itens = [
                    t
                    for t in itens
                    if digitos(t["contactPhone"]) == alvo
                    or digitos((self._acha("requesters", t["requesterId"]) or {}).get("phone"))
                    == alvo
                ]
            if query.get("updatedSince"):
                itens = [t for t in itens if t["updatedAt"] >= query["updatedSince"]]
            return paginar([self._ticket_externo(t) for t in itens], query)

    def responder_ticket(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`POST /tickets/:number/reply` {body}. Só registra no ticket (07 item T7: destino da resposta é desconhecido)."""
        body = corpo.get("body")
        if not isinstance(body, str) or not body.strip():
            raise invalido("body", "Campo obrigatório ausente: body.")
        with self.lock:
            t = self._ticket(args[0])
            reply = {"id": novo_id(), "body": body, "createdAt": agora_iso()}
            t["replies"].append(reply)
            t["updatedAt"] = reply["createdAt"]
        return reply

    def nota_ticket(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`POST /tickets/:number/notes` {body}: nota interna. Resposta não documentada (hipótese: igual ao /reply + `internal`)."""
        body = corpo.get("body")
        if not isinstance(body, str) or not body.strip():
            raise invalido("body", "Campo obrigatório ausente: body.")
        with self.lock:
            t = self._ticket(args[0])
            nota = {"id": novo_id(), "body": body, "internal": True, "createdAt": agora_iso()}
            t["notes"].append(nota)
            t["updatedAt"] = nota["createdAt"]
        return nota

    # --- conversas -------------------------------------------------------------

    def _conversa_externa(self, c: dict[str, Any]) -> dict[str, Any]:
        """Objeto `conversation` de docs/infodesk/04 (o mesmo do envelope de webhook)."""
        return {
            "id": c["id"],
            "channel": c["channel"],
            "status": c["status"],
            "subject": None,
            "botActive": c["botActive"],
            "handledBy": c["handledBy"],
            "assignedUserId": c["assignedUserId"],
            "departmentId": c["departmentId"],
            "clientId": c["clientId"],
            "requesterId": c["requesterId"],
            "resourceId": None,
            "ticketNumber": c["ticketNumber"],
            "contact": {
                "name": c["contact"]["name"],
                "phone": c["contact"]["phone"],
                "email": None,
                "groupJid": None,
            },
            "provider": "GUPSHUP",
            "lastMessageAt": c["lastMessageAt"],
            "lastMessagePreview": c["lastMessagePreview"],
            "unreadCount": c["unreadCount"],
            "sessionStartedAt": c["sessionStartedAt"],
            "createdAt": c["createdAt"],
            "updatedAt": c["updatedAt"],
        }

    @staticmethod
    def _mensagem_externa(m: dict[str, Any]) -> dict[str, Any]:
        saida = dict(m)
        if m.get("attachment") is not None:
            saida["attachment"] = dict(m["attachment"])
        return saida

    def listar_conversas(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`GET /conversations` com os filtros de docs/infodesk/04, paginado por cursor."""
        tab = query.get("tab")
        if tab is not None and tab not in ("entrada", "abertas", "encerrados", "todas"):
            raise invalido(
                "tab", "Parâmetro inválido: tab deve ser entrada, abertas, encerrados ou todas."
            )
        status = query.get("status")
        if status is not None and status not in ("PENDING", "IN_PROGRESS", "FINISHED"):
            raise invalido(
                "status", "Parâmetro inválido: status deve ser PENDING, IN_PROGRESS ou FINISHED."
            )
        bot = query.get("botActive")
        if bot is not None and bot not in ("true", "false"):
            raise invalido("botActive", "Parâmetro inválido: botActive deve ser true ou false.")
        with self.lock:
            itens = list(self.conversas.values())
        if tab == "entrada":  # sem dono
            itens = [c for c in itens if c["status"] != "FINISHED" and not c["assignedUserId"]]
        elif tab == "abertas":
            itens = [c for c in itens if c["status"] != "FINISHED"]
        elif tab == "encerrados":
            itens = [c for c in itens if c["status"] == "FINISHED"]
        if status:
            itens = [c for c in itens if c["status"] == status]
        if query.get("channel"):
            itens = [c for c in itens if c["channel"] == query["channel"]]
        if query.get("assignedUserId") == "none":
            itens = [c for c in itens if not c["assignedUserId"]]
        elif query.get("assignedUserId"):
            itens = [c for c in itens if c["assignedUserId"] == query["assignedUserId"]]
        if query.get("departmentId"):
            itens = [c for c in itens if c["departmentId"] == query["departmentId"]]
        if bot is not None:
            itens = [c for c in itens if c["botActive"] == (bot == "true")]
        if query.get("phone"):
            alvo = digitos(query["phone"])
            itens = [c for c in itens if digitos(c["contact"]["phone"]) == alvo]
        if query.get("updatedSince"):
            itens = [c for c in itens if c["updatedAt"] >= query["updatedSince"]]
        itens.sort(key=lambda c: c["lastMessageAt"] or c["createdAt"], reverse=True)
        with self.lock:
            return paginar([self._conversa_externa(c) for c in itens], query)

    def _conversa(self, conv_id: str, aberta: bool = False) -> dict[str, Any]:
        c = self.conversas.get(conv_id)
        if c is None:
            raise nao_encontrado("Conversa não encontrada.")
        if aberta and c["status"] == "FINISHED":
            # Política nossa (docs/infodesk/06): operação em conversa encerrada = NOT_FOUND.
            raise nao_encontrado("Conversa não encontrada ou encerrada.")
        return c

    def _janela_whatsapp(self, c: dict[str, Any]) -> dict[str, Any]:
        """Janela de 24 h (docs/infodesk/04). Formato dentro de GET /conversations/:id não é explicitado."""
        entradas = [m for m in c["messages"] if m["sender"] == "CLIENT"]
        ultima = entradas[-1]["createdAt"] if entradas else None
        expira = None
        aberta = False
        if ultima:
            fim = datetime.fromisoformat(ultima) + timedelta(hours=24)
            expira = agora_iso(fim)
            aberta = fim > datetime.now(UTC)
        return {
            "phone": c["contact"]["phone"],
            "open": aberta,
            "expiresAt": expira,
            "lastInboundAt": ultima,
            "conversationId": c["id"],
        }

    def ler_conversa(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`GET /conversations/:id`: conversa + `whatsappWindow` + `recentMessages` (últimas 50, sem notas internas)."""
        with self.lock:
            c = self._conversa(args[0])
            publicas = [self._mensagem_externa(m) for m in c["messages"] if not m["internal"]]
            return {
                **self._conversa_externa(c),
                "whatsappWindow": self._janela_whatsapp(c),
                "recentMessages": publicas[-50:],
            }

    def _gravar_mensagem(
        self,
        c: dict[str, Any],
        sender: str,
        body: str,
        tipo: str = "TEXT",
        sender_name: str | None = None,
        interna: bool = False,
        attachment: dict[str, Any] | None = None,
        status: str | None = None,
    ) -> dict[str, Any]:
        agora = agora_iso()
        msg = {
            "id": novo_id(),
            "conversationId": c["id"],
            "sender": sender,
            "senderName": sender_name,
            "type": tipo,
            "status": status,
            "body": body,
            "internal": interna,
            "deleted": False,
            "replyToId": None,
            "createdAt": agora,
            "attachment": attachment,
        }
        c["messages"].append(msg)
        c["updatedAt"] = agora
        if not interna:
            c["lastMessageAt"] = agora
            c["lastMessagePreview"] = body or f"[{tipo.lower()}]"
            c["unreadCount"] = c["unreadCount"] + 1 if sender == "CLIENT" else 0
        return msg

    def enviar_mensagem(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`POST /conversations/:id/messages` (docs/infodesk/04): `text` (+ `internal: true` = nota interna).

        `body` NÃO vale mais (era o guia antigo) → 400 VALIDATION_ERROR. `template` e `media`
        existem no Infodesk mas o mock só implementa `text` (fora da v1 da Sofia).
        """
        with self.lock:
            c = self._conversa(args[0], aberta=True)
            texto = corpo.get("text")
            if not isinstance(texto, str) or not texto.strip():
                if "template" in corpo or "media" in corpo:
                    raise invalido(
                        "text", "O mock só implementa 'text' (template e media estão fora da v1)."
                    )
                raise invalido(
                    "text", "Campo obrigatório ausente: text (o campo é 'text', não 'body')."
                )
            interna = corpo.get("internal", False)
            if not isinstance(interna, bool):
                raise invalido("internal", "Campo inválido: internal deve ser booleano.")
            msg = self._gravar_mensagem(
                c,
                "AGENT",  # "atendente, bot ou integração" (docs/infodesk/04)
                texto,
                sender_name=self.config["nome_chave"],
                interna=interna,
                status=None if interna else "queued",
            )
            saida = {"conversationId": c["id"], "messages": [self._mensagem_externa(msg)]}
        self.log(
            f"[mock] bot → conversa {args[0][:8]}…: {len(texto)} chars{' (nota interna)' if interna else ''}"
        )
        return saida

    def atribuir(self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]) -> Any:
        """`POST /conversations/:id/assign` {userId}: atendente assume (bot sai) e emite `conversation.assigned`.

        `null` desatribui (hipótese; o PDF não documenta — docs/infodesk/04): vai para a fila.
        """
        if "userId" not in corpo:
            raise invalido("userId", "Campo obrigatório ausente: userId.")
        user_id = corpo["userId"]
        if user_id is not None and self._acha("users", user_id) is None:
            raise referencia_invalida(f"userId inválido: atendente não encontrado ({user_id}).")
        with self.lock:
            c = self._conversa(args[0], aberta=True)
            env = self._assumir(c, user_id)
            externo = self._conversa_externa(c)
        self._entregar_se(env)
        return externo

    def _assumir(self, c: dict[str, Any], user_id: str | None) -> dict[str, Any] | None:
        """Atendente assume (ou desatribui). Segura o lock. Devolve o envelope de `conversation.assigned`."""
        c["botActive"] = False
        c["assignedUserId"] = user_id
        c["updatedAt"] = agora_iso()
        if user_id is None:
            c["handledBy"] = "QUEUE"
            c["status"] = "PENDING"
            self._evento_sistema(c, "atendente → nenhum (fila)")
            return None
        c["handledBy"] = "AGENT"
        c["status"] = "IN_PROGRESS"
        self._evento_sistema(c, "atendente → " + (self._nome("users", user_id) or user_id))
        # ver docs/infodesk/07 item W7: extras de conversation.assigned não documentados → sem extras.
        return self._evento("conversation.assigned", self._data_conversa(c))

    def transferir_departamento(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`POST /conversations/:id/department` {departmentId} — `null` remove. Não solta o bot (docs/infodesk/04)."""
        if "departmentId" not in corpo:
            raise invalido("departmentId", "Campo obrigatório ausente: departmentId.")
        dep_id = corpo["departmentId"]
        if dep_id is not None and self._acha("departments", dep_id) is None:
            raise referencia_invalida(
                f"departmentId inválido: departamento não encontrado ({dep_id})."
            )
        with self.lock:
            c = self._conversa(args[0], aberta=True)
            c["departmentId"] = dep_id
            c["updatedAt"] = agora_iso()
            self._evento_sistema(
                c, "departamento → " + (self._nome("departments", dep_id) or "nenhum")
            )
            return self._conversa_externa(c)

    def handoff(self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]) -> Any:
        """`POST /conversations/:id/handoff` {departmentId?, userId?, note} (docs/infodesk/08).

        O bot sai; a conversa volta para a fila (`QUEUE`) ou vai para o `userId` (`AGENT`); a
        `note` vira nota interna; emite `conversation.handoff`.
        """
        exigir_str_ou_none(corpo, "departmentId", "userId", "note")
        dep_id = corpo.get("departmentId")
        user_id = corpo.get("userId")
        note = corpo.get("note")
        if dep_id is not None and self._acha("departments", dep_id) is None:
            raise referencia_invalida(
                f"departmentId inválido: departamento não encontrado ({dep_id})."
            )
        if user_id is not None and self._acha("users", user_id) is None:
            raise referencia_invalida(f"userId inválido: atendente não encontrado ({user_id}).")
        with self.lock:
            c = self._conversa(args[0], aberta=True)
            c["botActive"] = False
            if dep_id is not None:
                c["departmentId"] = dep_id
            if user_id is not None:
                c["assignedUserId"] = user_id
                c["handledBy"] = "AGENT"
                c["status"] = "IN_PROGRESS"
            else:
                # ver docs/infodesk/08: sem userId pode ir para a "distribuição automática" da
                # organização (comportamento desconhecido) — o mock sempre volta para a fila.
                c["assignedUserId"] = None
                c["handledBy"] = "QUEUE"
                c["status"] = "PENDING"
            if note and note.strip():
                self._gravar_mensagem(
                    c, "AGENT", note, sender_name=self.config["nome_chave"], interna=True
                )
            c["updatedAt"] = agora_iso()
            self._evento_sistema(c, "handoff para humanos" + (f": {note}" if note else ""))
            env = self._evento("conversation.handoff", self._data_conversa(c, note=note))
            externo = self._conversa_externa(c)
        self._entregar_se(env)
        return externo

    def _finalizar(self, c: dict[str, Any]) -> dict[str, Any] | None:
        """Encerra a conversa (idempotente). Segura o lock. Devolve o envelope de `conversation.finished`."""
        if c["status"] == "FINISHED":
            return None
        c["status"] = "FINISHED"
        c["updatedAt"] = agora_iso()
        self._evento_sistema(c, "conversa encerrada")
        if not self.config["finish_emite_webhook"]:
            return None
        # `reason` ausente = atendente/API (docs/infodesk/05); ver docs/infodesk/07 item C5.
        return self._evento("conversation.finished", self._data_conversa(c))

    def encerrar(self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]) -> Any:
        """`POST /conversations/:id/finish` (sem corpo; sem mensagem ao cliente). Idempotente."""
        with self.lock:
            c = self._conversa(args[0])
            env = self._finalizar(c)
            externo = self._conversa_externa(c)
        self._entregar_se(env)
        return externo

    def vincular_solicitante(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`POST /conversations/:id/requester` {id}: vincula (traz o cliente junto); `null` desfaz."""
        if "id" not in corpo:
            raise invalido("id", "Campo obrigatório ausente: id.")
        exigir_str_ou_none(corpo, "id")
        req_id = corpo["id"]
        solicitante = None
        if req_id is not None:
            solicitante = self._acha("requesters", req_id)
            if solicitante is None:
                raise referencia_invalida(f"id inválido: solicitante não encontrado ({req_id}).")
        with self.lock:
            c = self._conversa(args[0], aberta=True)
            c["requesterId"] = req_id
            if solicitante is not None:
                c["clientId"] = solicitante.get("clientId")
            c["updatedAt"] = agora_iso()
            return self._conversa_externa(c)

    def _transcricao(self, c: dict[str, Any], periodo: str) -> str:
        agora = datetime.now(UTC)
        corte = {
            "session": c["sessionStartedAt"],
            "today": agora_iso(agora.replace(hour=0, minute=0, second=0, microsecond=0)),
            "24h": agora_iso(agora - timedelta(hours=24)),
            "all": "",
        }[periodo]
        linhas = []
        for m in c["messages"]:
            if m["internal"] or m["createdAt"] < corte:
                continue
            autor = "Cliente" if m["sender"] == "CLIENT" else (m["senderName"] or m["sender"])
            linhas.append(
                f"[{m['createdAt']}] {autor}: {m['body'] or '[' + m['type'].lower() + ']'}"
            )
        return "\n".join(linhas)

    def abrir_ticket_da_conversa(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`POST /conversations/:id/ticket` (docs/infodesk/03): abre o chamado com a transcrição, vinculado à conversa."""
        periodo = corpo.get("period", "session")
        if periodo not in PERIODOS:
            raise invalido(
                "period", f"Campo inválido: period deve ser um de {', '.join(PERIODOS)}."
            )
        finish = corpo.get("finish", False)
        if not isinstance(finish, bool):
            raise invalido("finish", "Campo inválido: finish deve ser booleano.")
        with self.lock:
            c = self._conversa(args[0], aberta=True)
            transcricao = self._transcricao(c, periodo)
            extra = f"\n\n--- Transcrição ({periodo}) ---\n{transcricao}" if transcricao else ""
            # ver docs/infodesk/07 item T12: conversa que já tem ticketNumber — o mock cria OUTRO
            # chamado e passa a apontar para ele (sem CONFLICT).
            ticket = self._abrir_ticket(corpo, conversa=c, extra_descricao=extra)
            c["ticketNumber"] = ticket["number"]
            c["updatedAt"] = ticket["createdAt"]
            env = None
            if finish:
                # A mensagem configurada com o número do chamado é do Desk; texto desconhecido →
                # só uma nota na UI do mock, nada na conversa.
                self._evento_sistema(
                    c, f"mensagem configurada com o nº do chamado #{ticket['number']}"
                )
                env = self._finalizar(c)
            externo = self._ticket_externo(ticket)
        self._entregar_se(env)
        return externo

    def vincular_ticket(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`POST /conversations/:id/link-ticket` {ticketNumber, finish?}: anexa a transcrição da sessão ao chamado."""
        numero = corpo.get("ticketNumber")
        if isinstance(numero, bool) or not isinstance(numero, int):
            raise invalido(
                "ticketNumber", "Campo obrigatório ausente ou inválido: ticketNumber (inteiro)."
            )
        finish = corpo.get("finish", False)
        if not isinstance(finish, bool):
            raise invalido("finish", "Campo inválido: finish deve ser booleano.")
        with self.lock:
            c = self._conversa(args[0], aberta=True)
            t = self.tickets.get(numero)
            if t is None:
                raise referencia_invalida(
                    f"ticketNumber inválido: chamado não encontrado ({numero})."
                )
            if t["conversationId"] not in (None, c["id"]):
                # "chamado preso a outro atendimento" (docs/infodesk/06)
                raise ErroApi(409, "CONFLICT", "O chamado está vinculado a outro atendimento.")
            t["conversationId"] = c["id"]
            transcricao = self._transcricao(c, "session")
            if transcricao:
                t["notes"].append(
                    {
                        "id": novo_id(),
                        "body": f"Transcrição da sessão:\n{transcricao}",
                        "internal": True,
                        "createdAt": agora_iso(),
                    }
                )
            t["updatedAt"] = c["updatedAt"] = agora_iso()
            c["ticketNumber"] = numero
            env = self._finalizar(c) if finish else None
            externo = self._conversa_externa(c)
        self._entregar_se(env)
        return externo

    def _nome(self, colecao: str, id_: Any) -> str | None:
        item = self._acha(colecao, id_) if id_ else None
        return item.get("name") or item.get("username") if item else None

    def _evento_sistema(self, c: dict[str, Any], texto: str) -> None:
        """Nota só para a UI (não aparece na API)."""
        c["eventos"].append({"texto": texto, "createdAt": agora_iso()})

    # --- lado "cliente" / atendente (UI / E2E) -----------------------------------

    def _conversa_do_telefone(self, telefone: str, nome: str) -> tuple[dict[str, Any], bool]:
        """Reusa a conversa não encerrada do telefone ou cria uma nova.

        ver docs/infodesk/07 item C5: depois do `finish`, nova mensagem abre OUTRA conversa (novo id).
        """
        for c in self.conversas.values():
            if c["contact"]["phone"] == telefone and c["status"] != "FINISHED":
                if nome:
                    c["contact"]["name"] = nome
                return c, False
        agora = agora_iso()
        c = {
            "id": novo_id(),
            "status": "PENDING",
            "channel": "WHATSAPP",
            "botActive": True,
            "handledBy": "EXTERNAL_BOT",  # chave com "Conduz o atendimento" (docs/infodesk/08)
            "assignedUserId": None,
            "departmentId": None,
            "clientId": None,
            "requesterId": None,
            "ticketNumber": None,
            "contact": {"name": nome or "Cliente", "phone": telefone},
            "lastMessageAt": None,
            "lastMessagePreview": None,
            "unreadCount": 0,
            "sessionStartedAt": agora,
            "createdAt": agora,
            "updatedAt": agora,
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
            raise invalido("telefone", "telefone é obrigatório.")
        if not audio and not (texto or "").strip():
            raise invalido("texto", "texto é obrigatório (ou use áudio).")
        with self.lock:
            c, nova = self._conversa_do_telefone(telefone, (nome or "").strip())
            if audio:
                # ver docs/infodesk/04: no WhatsApp real a mídia gera DUAS mensagens (rótulo +
                # arquivo); o mock emite só a do arquivo. Formato de `attachment` além de `url`:
                # ver docs/infodesk/07 item C9.
                msg = self._gravar_mensagem(
                    c,
                    "CLIENT",
                    "",
                    tipo="AUDIO",
                    attachment={"url": f"https://anexos.mock-infodesk.invalid/{novo_id()}.ogg"},
                )
            else:
                msg = self._gravar_mensagem(c, "CLIENT", texto.strip())
            envs = []
            if nova:
                envs.append(self._evento("conversation.started", self._data_conversa(c)))
            envs.append(self._evento("message.received", self._data_mensagem(c, msg)))
            conversa_id = c["id"]
            msg_id = msg["id"]
        self.log(
            f"[mock] cliente {mascarar_telefone(telefone)} → conversa {conversa_id[:8]}… ({'áudio' if audio else 'texto'})"
        )
        return {
            "conversationId": conversa_id,
            "messageId": msg_id,
            "novaConversa": nova,
            "entregas": [self._entregar(e) for e in envs],
        }

    def _conversa_aberta_do_telefone(self, telefone: str) -> dict[str, Any]:
        c = next(
            (
                c
                for c in self.conversas.values()
                if c["contact"]["phone"] == telefone and c["status"] != "FINISHED"
            ),
            None,
        )
        if c is None:
            raise nao_encontrado("Nenhuma conversa aberta para esse telefone.")
        return c

    def encerrar_pelo_telefone(self, telefone: str) -> dict[str, Any]:
        with self.lock:
            c = self._conversa_aberta_do_telefone(telefone)
        return self.encerrar((c["id"],), {}, {})

    def assumir_pelo_telefone(self, telefone: str, user_id: str | None) -> dict[str, Any]:
        """Botão "atendente assume": `conversation.assigned` + `handledBy: AGENT` (docs/infodesk/08)."""
        user_id = user_id or self.referencia["users"][0]["id"]
        if self._acha("users", user_id) is None:
            raise referencia_invalida(f"userId inválido: atendente não encontrado ({user_id}).")
        with self.lock:
            c = self._conversa_aberta_do_telefone(telefone)
        return self.atribuir((c["id"],), {}, {"userId": user_id})

    def reentregar_ultimo_evento(self, telefone: str | None) -> dict[str, Any]:
        """Reenvia o último evento (do telefone, se informado): MESMO `id`/`seq`, novo `X-Infominas-Delivery`."""
        with self.lock:
            candidatos = self.eventos
            if telefone:
                alvo = digitos(telefone)
                candidatos = [
                    e
                    for e in candidatos
                    if digitos(self._contato_do_evento(e).get("phone")) == alvo
                ]
            if not candidatos:
                raise nao_encontrado("Nenhum evento para reentregar.")
            env = candidatos[-1]
        return self._entregar(env)

    @staticmethod
    def _contato_do_evento(env: dict[str, Any]) -> dict[str, Any]:
        return (env["data"].get("contact") or {}) if isinstance(env.get("data"), dict) else {}

    def estado_para_ui(self, telefone: str | None) -> dict[str, Any]:
        with self.lock:
            conversa = None
            if telefone:
                alvo = digitos(telefone)
                das_conversas = [
                    c for c in self.conversas.values() if digitos(c["contact"]["phone"]) == alvo
                ]
                das_conversas.sort(key=lambda c: (c["status"] != "FINISHED", c["createdAt"]))
                if das_conversas:
                    c = das_conversas[-1]
                    conversa = self._conversa_externa(c)
                    conversa["messages"] = [self._mensagem_externa(m) for m in c["messages"]]
                    conversa["eventos"] = list(c["eventos"])
                    conversa["departmentName"] = self._nome("departments", c["departmentId"])
                    conversa["assignedUserName"] = self._nome("users", c["assignedUserId"])
            tickets = []
            for t in sorted(self.tickets.values(), key=lambda t: t["number"]):
                desk = self._acha("desks", t["deskId"])
                tickets.append(
                    {
                        "number": t["number"],
                        "title": t["title"],
                        "deskName": desk["name"] if desk else t["deskId"],
                        "status": self._status_do_ticket(t)["name"],
                        "contactPhone": t.get("contactPhone"),
                        "conversationId": t["conversationId"],
                        "replies": len(t["replies"]),
                        "notes": len(t["notes"]),
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
                    "semReentrega": self.config["sem_reentrega"],
                },
            }

    # --- eventos e webhooks -----------------------------------------------------

    def _data_conversa(self, c: dict[str, Any], **extras: Any) -> dict[str, Any]:
        """`data` dos eventos de conversa: `{conversation, contact, …extras}` (docs/infodesk/05)."""
        conversa = self._conversa_externa(c)
        return {"conversation": conversa, "contact": dict(conversa["contact"]), **extras}

    def _data_mensagem(self, c: dict[str, Any], msg: dict[str, Any]) -> dict[str, Any]:
        """`data` dos eventos de mensagem: `{conversation, contact, message}` (docs/infodesk/05)."""
        return {**self._data_conversa(c), "message": self._mensagem_externa(msg)}

    def _evento(self, evento: str, data: dict[str, Any]) -> dict[str, Any]:
        """Grava o evento no feed (`GET /events`) e devolve o envelope — a entrega é outra etapa.

        O `data` é um retrato do momento do evento; reentrega e feed repetem o mesmo retrato
        (ver docs/infodesk/07 item W9: o real pode refletir o estado atual).
        """
        with self.lock:
            self.seq += 1
            env = {
                "id": f"evt_{self.seq:012d}",
                "seq": self.seq,
                "event": evento,
                "createdAt": agora_iso(),
                "organizationId": self.config["org_id"],
                "data": data,
            }
            self.eventos.append(env)
            return env

    def listar_eventos(
        self, args: tuple[str, ...], query: dict[str, str], corpo: dict[str, Any]
    ) -> Any:
        """`GET /events?after=<seq>&limit=&types=&conversationId=&ticketNumber=` → `{data, nextAfter, hasMore}`."""
        bruto = query.get("after", "0") or "0"
        try:
            depois = int(bruto)
        except ValueError:
            raise invalido("after", "Parâmetro inválido: after deve ser o seq (inteiro).") from None
        if depois < 0:
            raise invalido("after", "Parâmetro inválido: after deve ser >= 0.")
        limite = limite_da_query(query, LIMITE_PADRAO, LIMITE_EVENTOS_MAX)
        tipos = {t.strip() for t in (query.get("types") or "").split(",") if t.strip()}
        with self.lock:
            itens = [e for e in self.eventos if e["seq"] > depois]
            if tipos:
                itens = [e for e in itens if e["event"] in tipos]
            if query.get("conversationId"):
                itens = [
                    e
                    for e in itens
                    if (e["data"].get("conversation") or {}).get("id") == query["conversationId"]
                ]
            if query.get("ticketNumber"):
                itens = [
                    e
                    for e in itens
                    if str((e["data"].get("ticket") or {}).get("number")) == query["ticketNumber"]
                ]
            pagina = itens[:limite]
            return {
                "data": json.loads(json.dumps(pagina)),
                "nextAfter": pagina[-1]["seq"] if pagina else depois,
                "hasMore": len(itens) > limite,
            }

    def _entregar_se(self, env: dict[str, Any] | None) -> None:
        if env is not None:
            self._entregar(env)

    def _entregar(self, env: dict[str, Any], tentativa: int = 0) -> dict[str, Any]:
        """POST assinado no gateway. Síncrono, timeout 8 s. Falha → registro + reentrega automática (se ligada).

        Headers (docs/infodesk/05, 5.4): `X-Infominas-Event`, `X-Infominas-Delivery` (id da ENTREGA,
        novo a cada tentativa), `X-Infominas-Timestamp` (segundos Unix) e `X-Infominas-Signature` =
        `sha256=HMAC(secret, "<ts>.<corpo bruto>")`. O `id` do envelope (evento) não muda.
        """
        raw = json.dumps(env, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        url = self.config["gateway_webhook_url"]
        delivery = novo_id()
        ts = str(int(time.time()))
        req = urllib.request.Request(url, data=raw, method="POST")  # noqa: S310 — URL vem da config do dev
        req.add_header("Content-Type", "application/json")
        req.add_header("X-Infominas-Event", env["event"])
        req.add_header("X-Infominas-Delivery", delivery)
        req.add_header("X-Infominas-Timestamp", ts)
        req.add_header("X-Infominas-Signature", assinar(self.config["webhook_secret"], ts, raw))
        data = env["data"]
        conversa_id = (data.get("conversation") or {}).get("id") if isinstance(data, dict) else None
        registro = {
            "id": env["id"],
            "seq": env["seq"],
            "event": env["event"],
            "conversationId": conversa_id,
            "at": env["createdAt"],
            "delivery": delivery,
            "tentativa": tentativa + 1,
        }
        try:
            with urllib.request.urlopen(req, timeout=WEBHOOK_TIMEOUT_S) as resp:  # noqa: S310 — URL vem da config do dev
                registro.update(ok=200 <= resp.status < 300, status=resp.status, erro=None)
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
            c = self.conversas.get(conversa_id) if conversa_id else None
            if not registro["ok"] and c is not None:
                self._evento_sistema(c, f"webhook {env['event']} falhou: {registro['erro']}")
        self.log(
            f"[mock] webhook {env['event']} (#{env['seq']}, tentativa {tentativa + 1}) → {url}: "
            + ("ok" if registro["ok"] else f"FALHA ({registro['erro']})")
        )
        if not registro["ok"]:
            self._agendar_reentrega(env, tentativa)
        return registro

    def _agendar_reentrega(self, env: dict[str, Any], tentativa: int) -> None:
        """Reentrega automática (30 s, 2 min, 10 min, 1 h, 6 h) — desligada com MOCK_SEM_REENTREGA=1."""
        atrasos = self.config["reentrega_atrasos_s"]
        if self.config["sem_reentrega"] or tentativa >= len(atrasos):
            return
        t = threading.Timer(atrasos[tentativa], self._entregar, args=(env, tentativa + 1))
        t.daemon = True
        with self.lock:
            self.timers = [x for x in self.timers if x.is_alive()]
            self.timers.append(t)
        t.start()


# ---------------------------------------------------------------------------
# Roteamento HTTP
# ---------------------------------------------------------------------------


class Rota(NamedTuple):
    metodo: str
    rx: re.Pattern[str]
    escopos: tuple[str, ...]
    handler: str
    status: int = 200
    # Rotas de referência não exigem escopo (docs/infodesk/01); com MOCK_REFERENCIA_EXIGE_ESCOPO exigem este.
    escopo_referencia: str | None = None


def _r(
    metodo: str,
    caminho: str,
    escopos: tuple[str, ...],
    handler: str,
    status: int = 200,
    escopo_referencia: str | None = None,
) -> Rota:
    return Rota(
        metodo, re.compile("^" + caminho + "$"), escopos, handler, status, escopo_referencia
    )


ID = r"([^/]+)"
# Códigos/corpos de sucesso são HIPÓTESE onde o PDF não os cita (docs/infodesk/03/04):
# 201 + recurso criado em POST que cria (ticket, reply, notes, messages); 200 + objeto nos demais.
ROTAS: list[Rota] = [
    _r("GET", "/me", (), "me"),
    _r("GET", "/desks", (), "listar_mesas", escopo_referencia="tickets:read"),
    _r("GET", f"/desks/{ID}", (), "ler_mesa", escopo_referencia="tickets:read"),
    _r("GET", "/catalog", (), "listar_catalogo", escopo_referencia="tickets:read"),
    _r("GET", "/users", (), "listar_usuarios", escopo_referencia="conversations:read"),
    _r("GET", "/departments", (), "listar_departamentos", escopo_referencia="conversations:read"),
    _r("GET", "/contacts/lookup", ("contacts:read",), "lookup_contato"),
    _r("GET", "/tickets", ("tickets:read",), "listar_tickets"),
    _r("POST", "/tickets", ("tickets:write",), "criar_ticket", 201),
    _r("GET", f"/tickets/{ID}", ("tickets:read",), "consultar_ticket"),
    _r("POST", f"/tickets/{ID}/reply", ("tickets:write",), "responder_ticket", 201),
    _r("POST", f"/tickets/{ID}/notes", ("tickets:write",), "nota_ticket", 201),
    _r("GET", "/conversations", ("conversations:read",), "listar_conversas"),
    _r("GET", f"/conversations/{ID}", ("conversations:read",), "ler_conversa"),
    _r("POST", f"/conversations/{ID}/messages", ("conversations:write",), "enviar_mensagem", 201),
    _r("POST", f"/conversations/{ID}/assign", ("conversations:write",), "atribuir"),
    _r(
        "POST",
        f"/conversations/{ID}/department",
        ("conversations:write",),
        "transferir_departamento",
    ),
    _r("POST", f"/conversations/{ID}/finish", ("conversations:write",), "encerrar"),
    _r("POST", f"/conversations/{ID}/handoff", ("conversations:write",), "handoff"),
    _r("POST", f"/conversations/{ID}/requester", ("conversations:write",), "vincular_solicitante"),
    _r(
        "POST",
        f"/conversations/{ID}/ticket",
        ("tickets:write", "conversations:write"),
        "abrir_ticket_da_conversa",
        201,
    ),
    _r("POST", f"/conversations/{ID}/link-ticket", ("conversations:write",), "vincular_ticket"),
    _r("GET", "/events", ("events:read",), "listar_eventos"),
]


class ServidorMock(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, endereco: tuple[str, int], mock: MockInfodesk):
        super().__init__(endereco, Handler)
        self.mock = mock


class Handler(BaseHTTPRequestHandler):
    server_version = "MockInfodesk/2.0"
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

    def _erro(self, exc: ErroApi) -> None:
        self._json(exc.corpo(), exc.status, exc.headers)

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
            raise invalido(
                None,
                f"Corpo inválido: Content-Length ausente, inválido ou acima de {CORPO_MAX_BYTES} bytes.",
            )
        raw = self._corpo_bruto
        if not raw.strip():
            return {}
        try:
            dados = json.loads(raw.decode("utf-8"))
        except (json.JSONDecodeError, UnicodeDecodeError):
            raise invalido(None, "Corpo inválido: esperado JSON.") from None
        if not isinstance(dados, dict):
            raise invalido(None, "Corpo inválido: esperado objeto JSON.")
        return dados

    def log_message(self, fmt: str, *args: Any) -> None:
        # Uma linha por requisição no stdout. Só o caminho, SEM query string
        # (/mock/estado?telefone=... e /contacts/lookup?phone=... têm PII — CLAUDE.md regra 5).
        # Em /mock/* sequências longas de dígitos são mascaradas por garantia; na API os
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
            raise nao_encontrado("Recurso não encontrado.")
        except ErroApi as exc:
            self._erro(exc)
        except Exception as exc:
            # Só o tipo: a mensagem da exceção pode ecoar trechos do corpo (PII).
            mock.log(f"[mock] ERRO interno em {metodo} {path}: {type(exc).__name__}")
            self._erro(ErroApi(500, "INTERNAL_ERROR", "Erro interno do mock."))

    def _rota_api(self, metodo: str, sub: str, query: dict[str, str]) -> None:
        mock = self.server.mock
        mock.autenticar(self.headers)
        mock.contar_para_429()
        do_caminho = [(r, r.rx.match(sub)) for r in ROTAS]
        do_caminho = [(r, m) for r, m in do_caminho if m]
        if not do_caminho:
            raise nao_encontrado("Recurso não encontrado.")
        achada = next(((r, m) for r, m in do_caminho if r.metodo == metodo), None)
        if achada is None:
            # ver docs/infodesk/06: o PDF não cita 405 nem tem `code` para ele → NOT_FOUND.
            raise nao_encontrado("Recurso não encontrado.")
        rota, match = achada
        if rota.escopo_referencia and mock.config["referencia_exige_escopo"]:
            mock.exigir_escopo(rota.escopo_referencia)
        for escopo in rota.escopos:
            mock.exigir_escopo(escopo)
        corpo = self._ler_json() if metodo == "POST" else {}
        resultado = getattr(mock, rota.handler)(match.groups(), query, corpo)
        self._json(resultado, rota.status)

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
        elif metodo == "POST" and path == "/mock/atendente/assume":
            b = self._ler_json()
            self._json(
                mock.assumir_pelo_telefone((b.get("telefone") or "").strip(), b.get("userId"))
            )
        elif metodo == "POST" and path == "/mock/reentregar":
            b = self._ler_json()
            self._json(mock.reentregar_ultimo_evento((b.get("telefone") or "").strip() or None))
        elif metodo == "POST" and path == "/mock/reset":
            mock.reset()
            self._json({"ok": True})
        else:
            raise nao_encontrado("Recurso não encontrado.")


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
  .BOT .bubble.nota { background:#3b3a1f; }
  .sys .bubble { background:transparent; color:var(--mut); font-size:12px; font-style:italic; text-align:center; max-width:90%; }
  .bubble small { display:block; color:#cfd8dc99; font-size:10px; margin-top:4px; }
  aside { width:340px; background:var(--panel); border-left:1px solid #0008; padding:14px; overflow-y:auto; font-size:13px; }
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
    <button id="assume" class="sec" title="faz o papel do atendente: assume a conversa (handledBy AGENT, bot fora) e emite conversation.assigned">Atendente assume</button>
    <button id="encerrar" class="sec" title="faz o papel do atendente: encerra a conversa e emite conversation.finished">Encerrar conversa</button>
    <button id="reentregar" class="sec" title="reenvia o último evento: mesmo id (dedup), novo X-Infominas-Delivery">Reentregar último evento</button>
    <span class="pill" id="info">—</span>
  </header>
  <div class="hint" id="hint">Escreva como o <b>cliente</b> no WhatsApp: a primeira mensagem cria a conversa (<code>conversation.started</code> + <code>message.received</code> assinados para o gateway, <code>handledBy: EXTERNAL_BOT</code>). As respostas que o gateway enviar por <code>POST /conversations/:id/messages</code> aparecem como bolhas do bot (notas internas em amarelo). <b>🎤 Áudio</b> envia <code>message.received</code> com <code>type: AUDIO</code> e <code>body</code> vazio.</div>
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
    <button id="audio" class="sec" title="message.received com type AUDIO e body vazio">🎤 Áudio</button>
  </footer>

<script>
const $ = (id) => document.getElementById(id);
const log = $("log");
let vistos = new Set();
let convAtual = null;

function telefone() { return $("telefone").value.trim(); }
function nome() { return $("nome").value.trim(); }

function bubble(cls, text, meta, extra) {
  const row = document.createElement("div");
  row.className = "row " + cls;
  const b = document.createElement("div");
  b.className = "bubble" + (extra ? " " + extra : "");
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
    ["id", c.id], ["status", c.status], ["handledBy", c.handledBy], ["botActive", String(c.botActive)],
    ["canal", c.channel],
    ["departamento", c.departmentName || (c.departmentId ? c.departmentId : "—")],
    ["atendente", c.assignedUserName || (c.assignedUserId ? c.assignedUserId : "—")],
    ["chamado", c.ticketNumber ? "#" + c.ticketNumber : "—"],
    ["última msg", c.lastMessageAt || "—"],
  ];
  dl.innerHTML = linhas.map(([k, v]) => `<dt>${k}</dt><dd>${esc(v)}</dd>`).join("");
  $("info").textContent = `conversa ${c.id.slice(0, 8)}… · ${c.status} · ${c.handledBy}`;
}

function renderTickets(ts) {
  const el = $("tickets");
  if (!ts.length) { el.innerHTML = '<span class="vazio">nenhum</span>'; return; }
  el.innerHTML = ts.map(t =>
    `<div class="ticket"><b>#${t.number}</b> ${esc(t.title)}<br><span class="pill">mesa: ${esc(t.deskName)} · ${esc(t.status)}${t.contactPhone ? " · " + esc(t.contactPhone) : ""}${t.replies ? " · " + t.replies + " reply" : ""}${t.notes ? " · " + t.notes + " nota(s)" : ""}</span></div>`
  ).join("");
}

function renderEntregas(es) {
  const el = $("entregas");
  if (!es.length) { el.innerHTML = '<span class="vazio">nenhum</span>'; return; }
  el.innerHTML = es.slice().reverse().map(e =>
    `<div class="entrega ${e.ok ? "ok" : "falha"}">${esc(e.event)} <span class="pill">#${e.seq} · ${esc(e.id)} · tent. ${e.tentativa} · ${e.at.slice(11, 19)}${e.status ? " · " + e.status : ""}</span>${e.ok ? "" : `<span class="erro">${esc(e.erro || "")}</span>`}</div>`
  ).join("");
}

function esc(s) { return String(s ?? "").replace(/[&<>"]/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c])); }

function rotulo(m) {
  if (m.type === "AUDIO") return "🎤 (áudio — body vazio)";
  if (m.type !== "TEXT") return `(${m.type})`;
  return m.body;
}

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
        if (it.m) {
          const m = it.m;
          const quem = m.sender === "CLIENT" ? "CLIENT" : "BOT";
          const meta = `${m.sender}${m.senderName ? " (" + m.senderName + ")" : ""}${m.internal ? " · nota interna" : ""} · ${m.createdAt.slice(11, 19)}`;
          bubble(quem, (m.internal ? "🔒 " : "") + rotulo(m), meta, m.internal ? "nota" : "");
        } else bubble("sys", "📌 " + it.e.texto);
      }
    }
    renderConv(c);
    renderTickets(d.tickets);
    renderEntregas(d.entregas);
    $("cfg").innerHTML = `<dt>webhook</dt><dd>${esc(d.config.gatewayWebhookUrl)}</dd><dt>escopos</dt><dd>${esc(d.config.escopos.join(", "))}</dd><dt>429 a cada</dt><dd>${d.config.aCada429 || "nunca"}</dd><dt>reentrega auto</dt><dd>${d.config.semReentrega ? "desligada" : "ligada"}</dd>`;
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

async function assume() {
  if (!telefone()) return;
  await post("/mock/atendente/assume", { telefone: telefone() });
  poll();
}

async function reentregar() {
  if (!telefone()) return;
  await post("/mock/reentregar", { telefone: telefone() });
  poll();
}

$("send").addEventListener("click", enviar);
$("audio").addEventListener("click", audio);
$("encerrar").addEventListener("click", encerrar);
$("assume").addEventListener("click", assume);
$("reentregar").addEventListener("click", reentregar);
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
    print("┌─ Mock Infodesk (API v1, 2026-10-19) ───────────────────────")
    print(f"│ UI:            http://localhost:{cfg['port']}")
    print(
        f"│ API:           http://localhost:{cfg['port']}/api/integration/v1  (INFODESK_BASE_URL=http://localhost:{cfg['port']}/api)"
    )
    print(
        f"│ Webhook→gw:    {cfg['gateway_webhook_url']}  (X-Infominas-Timestamp + X-Infominas-Signature com MOCK_WEBHOOK_SECRET)"
    )
    print(
        f"│ Chave:         {cfg['api_key'][:8]}…  escopos: {', '.join(cfg['escopos']) or '(nenhum)'}"
    )
    print(
        f"│ 429 a cada:    {cfg['a_cada_429'] or 'nunca'}  ·  reentrega automática: {'desligada' if cfg['sem_reentrega'] else 'ligada'}"
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
