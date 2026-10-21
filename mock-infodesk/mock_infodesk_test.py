#!/usr/bin/env python3
"""Testes do mock-infodesk (stdlib `unittest`, sem rede externa).

Sobe o mock numa porta livre em thread e um "gateway" falso que recebe os
webhooks, e exercita a API v1 de 2026-10-19 (docs/infodesk/01–09): 401/403/404
com `code`, objeto `ticket` e `conversation` como na doc, `text` (e não `body`)
nas mensagens, rotas novas (/me, /desks/:id, /catalog, /contacts/lookup,
/conversations/:id/{ticket,link-ticket,handoff,requester}, /tickets/:n/notes,
/tickets?phone=, /events), webhooks assinados com timestamp, envelope com `id`
estável, reentrega (manual e automática) e o feed /events.

Rodar:  python -I mock-infodesk/mock_infodesk_test.py
"""

from __future__ import annotations

import hashlib
import hmac
import json
import socket
import sys
import threading
import time
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

# `python -I` não põe a pasta do script no sys.path.
sys.path.insert(0, str(Path(__file__).resolve().parent))

import mock_infodesk

CHAVE = "ik_test_unit"
SEGREDO = "segredo-unit"
DESK_ABERTURA = "11111111-1111-4111-8111-111111111101"
DESK_FINANCEIRO = "11111111-1111-4111-8111-111111111103"
DESK_INCIDENTE_EXTERNO = "11111111-1111-4111-8111-111111111105"  # tem catálogo (itens de teste)
DESK_N1 = "11111111-1111-4111-8111-111111111115"
DEP_FINANCEIRO = "22222222-2222-4222-8222-222222222202"
USER_ANA = "33333333-3333-4333-8333-333333333301"
REQ_MARIA = "aaaaaaaa-aaaa-4aaa-8aaa-000000000001"  # +5531990000100, cliente ACME
CLIENTE_ACME = "99999999-9999-4999-8999-000000000001"
FONE_MARIA = "+5531990000100"

# docs/infodesk/09: nomes EXATOS das 15 mesas
MESAS = [
    "Abertura de Ticket",
    "Cadastros de Usuário e E-mail - Nefroclinicas",
    "Financeiro",
    "Incidente",
    "Incidente Externo",
    "Incidente Interno",
    "Incidente Remoto",
    "Monitoramento",
    "Nefroclinicas",
    "Projetos",
    "Solicitações",
    "Solicitações Externo",
    "Solicitações Interno",
    "Solicitações Remoto",
    "Suporte Tecnico N1",
]
# docs/infodesk/06: tabela de códigos
CODES_DOCUMENTADOS = {
    "VALIDATION_ERROR",
    "INVALID_REFERENCE",
    "UNAUTHORIZED",
    "FORBIDDEN",
    "NOT_FOUND",
    "CONFLICT",
    "RATE_LIMITED",
    "INTERNAL_ERROR",
}
CAMPOS_CONVERSA = {
    "id", "channel", "status", "subject", "botActive", "handledBy", "assignedUserId",
    "departmentId", "clientId", "requesterId", "resourceId", "ticketNumber", "contact",
    "provider", "lastMessageAt", "lastMessagePreview", "unreadCount", "sessionStartedAt",
    "createdAt", "updatedAt",
}  # fmt: skip
CAMPOS_MENSAGEM = {
    "id", "conversationId", "sender", "senderName", "type", "status", "body", "internal",
    "deleted", "replyToId", "createdAt", "attachment",
}  # fmt: skip
CAMPOS_TICKET = {
    "id", "number", "title", "description", "channel", "ticketType", "impact", "urgency",
    "priorityLevel", "desk", "status", "stage", "priority", "catalogItemId", "client",
    "requester", "resourceId", "responsible", "contactPhone", "conversationId", "isClosed",
    "closedAt", "isDraft", "serviceDueAt", "solutionDueAt", "firstRespondedAt", "customValues",
    "createdAt", "updatedAt",
}  # fmt: skip


# ---------------------------------------------------------------------------
# Gateway falso: guarda cada entrega (headers + corpo bruto) e avisa quem espera
# ---------------------------------------------------------------------------


class GatewayFalso(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self) -> None:
        super().__init__(("127.0.0.1", 0), _HandlerGateway)
        self.entregas: list[dict[str, Any]] = []
        self.cond = threading.Condition()
        self.status = 200

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.server_address[1]}/webhook"

    def esperar(self, quantas: int, timeout: float = 5.0) -> list[dict[str, Any]]:
        with self.cond:
            self.cond.wait_for(lambda: len(self.entregas) >= quantas, timeout=timeout)
            return list(self.entregas)

    def limpar(self) -> None:
        with self.cond:
            self.entregas.clear()


class _HandlerGateway(BaseHTTPRequestHandler):
    server: GatewayFalso  # type: ignore[assignment]

    def log_message(self, fmt: str, *args: Any) -> None:
        return

    def do_POST(self) -> None:
        raw = self.rfile.read(int(self.headers.get("Content-Length") or 0))
        with self.server.cond:
            self.server.entregas.append(
                {
                    "path": self.path,
                    "event": self.headers.get("X-Infominas-Event"),
                    "delivery": self.headers.get("X-Infominas-Delivery"),
                    "timestamp": self.headers.get("X-Infominas-Timestamp"),
                    "signature": self.headers.get("X-Infominas-Signature"),
                    "content_type": self.headers.get("Content-Type"),
                    "raw": raw,
                    "json": json.loads(raw.decode("utf-8")),
                }
            )
            self.server.cond.notify_all()
        self.send_response(self.server.status)
        self.send_header("Content-Length", "0")
        self.end_headers()


# ---------------------------------------------------------------------------
# Cliente HTTP mínimo
# ---------------------------------------------------------------------------


class Resposta:
    def __init__(self, status: int, headers: Any, corpo: bytes):
        self.status = status
        self.headers = headers
        self.corpo = corpo

    @property
    def json(self) -> Any:
        return json.loads(self.corpo.decode("utf-8")) if self.corpo else None


def chamar(
    base: str,
    metodo: str,
    caminho: str,
    corpo: Any = None,
    chave: str | None = CHAVE,
    header_chave: str = "Authorization",
) -> Resposta:
    dados = None if corpo is None else json.dumps(corpo).encode("utf-8")
    req = urllib.request.Request(base + caminho, data=dados, method=metodo)  # noqa: S310
    req.add_header("Content-Type", "application/json")
    if chave is not None:
        if header_chave == "Authorization":
            req.add_header("Authorization", f"Bearer {chave}")
        else:
            req.add_header(header_chave, chave)
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:  # noqa: S310
            return Resposta(resp.status, resp.headers, resp.read())
    except urllib.error.HTTPError as exc:
        with exc:
            return Resposta(exc.code, exc.headers, exc.read())


def assinatura_esperada(timestamp: str, raw: bytes) -> str:
    """docs/infodesk/05 (5.4): HMAC-SHA256(secret, "<ts>.<corpo bruto>"), hex com prefixo `sha256=`."""
    msg = timestamp.encode() + b"." + raw
    return "sha256=" + hmac.new(SEGREDO.encode(), msg, hashlib.sha256).hexdigest()


# ---------------------------------------------------------------------------
# Testes
# ---------------------------------------------------------------------------


class TestMockInfodesk(unittest.TestCase):
    gateway: GatewayFalso
    servidor: mock_infodesk.ServidorMock
    base: str
    api: str

    @classmethod
    def setUpClass(cls) -> None:
        cls.gateway = GatewayFalso()
        threading.Thread(target=cls.gateway.serve_forever, daemon=True).start()
        cfg = mock_infodesk.config_do_ambiente()
        cfg.update(
            api_key=CHAVE, webhook_secret=SEGREDO, gateway_webhook_url=cls.gateway.url, a_cada_429=0
        )
        cfg["escopos"] = list(mock_infodesk.ESCOPOS_TODOS)
        cls.servidor = mock_infodesk.criar_servidor(cfg, host="127.0.0.1", port=0)
        cls.servidor.mock.log = lambda *_a, **_k: None  # silencia o stdout nos testes
        threading.Thread(target=cls.servidor.serve_forever, daemon=True).start()
        cls.base = f"http://127.0.0.1:{cls.servidor.server_address[1]}"
        cls.api = cls.base + "/api/integration/v1"

    @classmethod
    def tearDownClass(cls) -> None:
        cls.servidor.shutdown()
        cls.servidor.server_close()
        cls.gateway.shutdown()
        cls.gateway.server_close()

    def setUp(self) -> None:
        self.mock = self.servidor.mock
        self.mock.reset()
        self.mock.config.update(
            a_cada_429=0,
            escopos=list(mock_infodesk.ESCOPOS_TODOS),
            sem_reentrega=True,  # os testes de reentrega automática ligam por conta própria
            reentrega_atrasos_s=list(mock_infodesk.REENTREGA_ATRASOS_S),
            finish_emite_webhook=True,
            referencia_exige_escopo=False,
            gateway_webhook_url=self.gateway.url,
        )
        self.gateway.limpar()
        self.gateway.status = 200

    # --- helpers ------------------------------------------------------------

    def cliente_escreve(
        self,
        telefone: str = "+5531990000001",
        texto: str = "Bom dia, preciso de ajuda",
        nome: str = "Maria",
    ) -> dict[str, Any]:
        r = chamar(
            self.base,
            "POST",
            "/mock/cliente/mensagem",
            {"telefone": telefone, "nome": nome, "texto": texto},
            chave=None,
        )
        self.assertEqual(r.status, 201, r.corpo)
        return r.json

    def conversa_nova(self, telefone: str = "+5531990000001", nome: str = "Maria") -> str:
        """Cliente escreve e o gateway falso consome os 2 webhooks iniciais."""
        conv = self.cliente_escreve(telefone, nome=nome)["conversationId"]
        self.gateway.esperar(2)
        self.gateway.limpar()
        return conv

    def assert_erro(self, r: Resposta, status: int, code: str | None = None) -> dict[str, Any]:
        self.assertEqual(r.status, status, r.corpo)
        self.assertEqual(r.headers.get("Content-Type"), "application/json; charset=utf-8")
        corpo = r.json
        self.assertEqual(corpo["statusCode"], status)
        self.assertIn(corpo["code"], CODES_DOCUMENTADOS)  # docs/infodesk/06
        if code is not None:
            self.assertEqual(corpo["code"], code)
        self.assertIsInstance(corpo["message"], str)
        self.assertTrue(corpo["message"])
        return corpo

    def prioridade(self, desk_id: str, nome: str = "Alta") -> str:
        desk = chamar(self.api, "GET", f"/desks/{desk_id}").json
        return next(p["id"] for p in desk["priorities"] if p["name"] == nome)

    # --- autenticação / autorização -----------------------------------------

    def test_401_sem_chave(self) -> None:
        self.assert_erro(chamar(self.api, "GET", "/desks", chave=None), 401, "UNAUTHORIZED")

    def test_401_chave_errada(self) -> None:
        self.assert_erro(
            chamar(self.api, "GET", "/desks", chave="ik_live_errada"), 401, "UNAUTHORIZED"
        )

    def test_chave_aceita_por_bearer_e_por_x_api_key(self) -> None:
        self.assertEqual(chamar(self.api, "GET", "/desks").status, 200)
        self.assertEqual(chamar(self.api, "GET", "/desks", header_chave="X-Api-Key").status, 200)

    def test_403_por_escopo_com_code_forbidden(self) -> None:
        self.mock.config["escopos"] = ["tickets:read", "conversations:read", "conversations:write"]
        erro = self.assert_erro(
            chamar(self.api, "POST", "/tickets", {"deskId": DESK_N1, "title": "x"}),
            403,
            "FORBIDDEN",
        )
        self.assertEqual(erro["message"], "Escopo insuficiente. Necessário: tickets:write.")
        # Os de referência não exigem escopo (docs/infodesk/01, 07 item A3).
        self.mock.config["escopos"] = []
        self.assertEqual(chamar(self.api, "GET", "/departments").status, 200)
        self.assertEqual(chamar(self.api, "GET", "/me").status, 200)
        self.assert_erro(chamar(self.api, "GET", "/conversations"), 403, "FORBIDDEN")
        self.assert_erro(chamar(self.api, "GET", "/events"), 403, "FORBIDDEN")
        self.assert_erro(
            chamar(self.api, "GET", "/contacts/lookup?phone=%2B5531"), 403, "FORBIDDEN"
        )

    def test_escopo_coringa_libera_tudo(self) -> None:
        self.mock.config["escopos"] = ["*"]
        self.assertEqual(chamar(self.api, "GET", "/events").status, 200)

    def test_abrir_ticket_da_conversa_exige_os_dois_escopos(self) -> None:
        conv = self.conversa_nova()
        corpo = {"deskId": DESK_N1, "title": "x"}
        for faltando in ("tickets:write", "conversations:write"):
            self.mock.config["escopos"] = [e for e in mock_infodesk.ESCOPOS_TODOS if e != faltando]
            erro = self.assert_erro(
                chamar(self.api, "POST", f"/conversations/{conv}/ticket", corpo), 403, "FORBIDDEN"
            )
            self.assertIn(faltando, erro["message"])

    def test_404_rota_desconhecida_no_formato_de_erro(self) -> None:
        self.assert_erro(chamar(self.api, "GET", "/priorities"), 404, "NOT_FOUND")

    def test_me(self) -> None:
        me = chamar(self.api, "GET", "/me").json
        self.assertEqual(
            set(me), {"organizationId", "keyId", "name", "scopes", "handlesConversations"}
        )
        self.assertEqual(me["organizationId"], self.mock.config["org_id"])
        self.assertEqual(me["name"], self.mock.config["nome_chave"])
        self.assertEqual(me["scopes"], list(mock_infodesk.ESCOPOS_TODOS))
        self.assertIs(me["handlesConversations"], True)
        self.assert_erro(chamar(self.api, "GET", "/me", chave=None), 401, "UNAUTHORIZED")

    # --- referência ----------------------------------------------------------

    def test_dados_de_referencia(self) -> None:
        desks = chamar(self.api, "GET", "/desks").json
        self.assertEqual([d["name"] for d in desks], MESAS)  # 15 mesas, nomes exatos (09)
        self.assertTrue(all(set(d) == {"id", "name"} for d in desks))
        self.assertEqual(len({d["id"] for d in desks}), 15)
        deps = chamar(self.api, "GET", "/departments").json
        self.assertEqual({d["name"] for d in deps}, {"Suporte", "Financeiro", "Atendimento"})
        users = chamar(self.api, "GET", "/users").json
        self.assertEqual(len(users), 2)
        self.assertTrue(
            all(set(u) == {"id", "username", "email", "type", "departmentIds"} for u in users)
        )

    def test_mesa_com_statuses_etapas_prioridades_e_catalogo(self) -> None:
        for d in chamar(self.api, "GET", "/desks").json:
            mesa = chamar(self.api, "GET", f"/desks/{d['id']}").json
            self.assertEqual(
                set(mesa), {"id", "name", "statuses", "stages", "priorities", "catalogItems"}
            )
            self.assertEqual(mesa["name"], d["name"])
            # prioridades por mesa (Baixa, Média, Alta, Urgente), ids próprios de cada mesa
            self.assertEqual(
                [p["name"] for p in mesa["priorities"]], ["Baixa", "Média", "Alta", "Urgente"]
            )
            self.assertTrue(
                all(
                    set(s) == {"id", "name", "isOpen", "isClose", "isCanceled"}
                    for s in mesa["statuses"]
                )
            )
            self.assertTrue(any(s["isOpen"] for s in mesa["statuses"]))
            self.assertTrue(any(s["isClose"] for s in mesa["statuses"]))
            self.assertTrue(any(s["isCanceled"] for s in mesa["statuses"]))
            self.assertTrue(all(set(e) == {"id", "name"} for e in mesa["stages"]))
        ids_alta = {self.prioridade(d["id"]) for d in chamar(self.api, "GET", "/desks").json}
        self.assertEqual(len(ids_alta), 15)
        self.assert_erro(chamar(self.api, "GET", "/desks/nao-existe"), 404, "NOT_FOUND")

    def test_catalogo_por_mesa(self) -> None:
        itens = chamar(self.api, "GET", f"/catalog?deskId={DESK_INCIDENTE_EXTERNO}").json
        self.assertTrue(itens)
        self.assertTrue(all({"id", "name"} <= set(i) for i in itens))
        self.assertEqual(
            itens, chamar(self.api, "GET", f"/desks/{DESK_INCIDENTE_EXTERNO}").json["catalogItems"]
        )
        self.assertEqual(chamar(self.api, "GET", f"/catalog?deskId={DESK_ABERTURA}").json, [])
        self.assert_erro(chamar(self.api, "GET", "/catalog"), 400, "VALIDATION_ERROR")
        self.assert_erro(chamar(self.api, "GET", "/catalog?deskId=x"), 404, "NOT_FOUND")

    # --- tickets ---------------------------------------------------------------

    def test_criar_ticket_valida_obrigatorios_com_code(self) -> None:
        erro = self.assert_erro(
            chamar(self.api, "POST", "/tickets", {"title": "sem mesa"}), 400, "VALIDATION_ERROR"
        )
        self.assertIn("deskId", erro["message"])
        self.assertEqual(erro["errors"][0]["field"], "deskId")
        erro = self.assert_erro(
            chamar(self.api, "POST", "/tickets", {"deskId": DESK_N1}), 400, "VALIDATION_ERROR"
        )
        self.assertIn("title", erro["message"])
        self.assert_erro(
            chamar(self.api, "POST", "/tickets", {"deskId": "nao-existe", "title": "x"}),
            400,
            "INVALID_REFERENCE",
        )
        self.assert_erro(
            chamar(
                self.api, "POST", "/tickets", {"deskId": DESK_N1, "title": "x", "priorityId": "x"}
            ),
            400,
            "INVALID_REFERENCE",
        )
        self.assert_erro(
            chamar(
                self.api, "POST", "/tickets", {"deskId": DESK_N1, "title": "x", "channel": "FAX"}
            ),
            400,
            "VALIDATION_ERROR",
        )

    def test_criar_e_consultar_ticket_como_na_doc_03(self) -> None:
        prio = self.prioridade(DESK_N1, "Alta")
        r = chamar(
            self.api,
            "POST",
            "/tickets",
            {
                "deskId": DESK_N1,
                "title": "Impressora não imprime",
                "description": "Cliente relata erro ao imprimir.",
                "channel": "WHATSAPP",
                "contactPhone": "+5531990000000",
                "priorityId": prio,
                "requesterId": REQ_MARIA,
            },
        )
        self.assertEqual(r.status, 201, r.corpo)
        t = r.json
        self.assertEqual(set(t), CAMPOS_TICKET)
        self.assertIsInstance(t["number"], int)  # `number` é SEMPRE inteiro (03)
        self.assertEqual(t["number"], 1001)
        self.assertEqual(t["desk"], {"id": DESK_N1, "name": "Suporte Tecnico N1"})
        self.assertEqual(t["channel"], "WHATSAPP")
        self.assertEqual(set(t["status"]), {"id", "name", "isOpen", "isClose", "isCanceled"})
        self.assertEqual(t["status"]["name"], "Aberto")
        self.assertIs(t["status"]["isOpen"], True)
        self.assertIs(t["status"]["isClose"], False)
        self.assertIs(t["status"]["isCanceled"], False)
        self.assertIs(t["isClosed"], False)
        self.assertIsNone(t["closedAt"])
        self.assertEqual(t["priority"], {"id": prio, "name": "Alta"})
        self.assertEqual(t["stage"]["name"], "Triagem")
        self.assertEqual(t["requester"]["id"], REQ_MARIA)
        self.assertEqual(set(t["requester"]), {"id", "name", "email", "phone"})
        self.assertEqual(
            t["client"], {"id": CLIENTE_ACME, "name": "ACME Ltda"}
        )  # o cliente vem junto
        self.assertIsNone(t["conversationId"])
        self.assertEqual(t["contactPhone"], "+5531990000000")
        self.assertEqual(
            chamar(self.api, "POST", "/tickets", {"deskId": DESK_N1, "title": "outro"}).json[
                "number"
            ],
            1002,
        )

        g = chamar(self.api, "GET", f"/tickets/{t['number']}")
        self.assertEqual(g.status, 200)
        self.assertEqual(g.json["title"], "Impressora não imprime")
        self.assertEqual(set(g.json), CAMPOS_TICKET | {"messages"})  # histórico público (03)
        self.assertEqual(g.json["messages"], [])
        self.assert_erro(chamar(self.api, "GET", "/tickets/9999"), 404, "NOT_FOUND")
        self.assert_erro(chamar(self.api, "GET", "/tickets/abc"), 404, "NOT_FOUND")

    def test_number_e_sempre_inteiro(self) -> None:
        # Não existe mais opção para devolver string (docs/infodesk/03 e 07 item T1).
        self.assertNotIn("number_como_string", self.mock.config)
        t = chamar(self.api, "POST", "/tickets", {"deskId": DESK_N1, "title": "x"}).json
        self.assertIs(type(t["number"]), int)
        self.assertIs(type(chamar(self.api, "GET", "/tickets/1001").json["number"]), int)
        itens = chamar(self.api, "GET", "/tickets").json["data"]
        self.assertTrue(all(type(i["number"]) is int for i in itens))

    def test_reply_do_ticket(self) -> None:
        t = chamar(self.api, "POST", "/tickets", {"deskId": DESK_N1, "title": "x"}).json
        self.assert_erro(
            chamar(self.api, "POST", f"/tickets/{t['number']}/reply", {}), 400, "VALIDATION_ERROR"
        )
        r = chamar(
            self.api, "POST", f"/tickets/{t['number']}/reply", {"body": "Já estamos verificando."}
        )
        self.assertEqual(r.status, 201)
        self.assertEqual(r.json["body"], "Já estamos verificando.")
        self.assert_erro(
            chamar(self.api, "POST", "/tickets/777/reply", {"body": "x"}), 404, "NOT_FOUND"
        )
        # a resposta é pública: entra no histórico (`messages`)
        msgs = chamar(self.api, "GET", f"/tickets/{t['number']}").json["messages"]
        self.assertEqual([m["body"] for m in msgs], ["Já estamos verificando."])

    def test_nota_interna_do_ticket(self) -> None:
        t = chamar(self.api, "POST", "/tickets", {"deskId": DESK_N1, "title": "x"}).json
        n = t["number"]
        self.assert_erro(
            chamar(self.api, "POST", f"/tickets/{n}/notes", {}), 400, "VALIDATION_ERROR"
        )
        r = chamar(
            self.api, "POST", f"/tickets/{n}/notes", {"body": "Cliente reiniciou o roteador."}
        )
        self.assertEqual(r.status, 201, r.corpo)
        self.assertEqual(r.json["body"], "Cliente reiniciou o roteador.")
        self.assertIs(r.json["internal"], True)
        self.assert_erro(
            chamar(self.api, "POST", "/tickets/777/notes", {"body": "x"}), 404, "NOT_FOUND"
        )
        # nota interna não aparece no histórico público
        self.assertEqual(chamar(self.api, "GET", f"/tickets/{n}").json["messages"], [])
        self.assertEqual(self.mock.tickets[n]["notes"][0]["body"], "Cliente reiniciou o roteador.")

    def test_listar_tickets_por_telefone_status_e_cursor(self) -> None:
        a = chamar(
            self.api,
            "POST",
            "/tickets",
            {"deskId": DESK_N1, "title": "A", "contactPhone": "+5531990000001"},
        ).json
        b = chamar(
            self.api,
            "POST",
            "/tickets",
            {"deskId": DESK_FINANCEIRO, "title": "B", "requesterId": REQ_MARIA},
        ).json
        c = chamar(
            self.api,
            "POST",
            "/tickets",
            {"deskId": DESK_N1, "title": "C", "contactPhone": "+5531990000001"},
        ).json
        lista = chamar(self.api, "GET", "/tickets").json
        self.assertEqual(set(lista), {"data", "nextCursor"})
        self.assertEqual(
            [t["number"] for t in lista["data"]], [c["number"], b["number"], a["number"]]
        )
        self.assertIsNone(lista["nextCursor"])
        # phone= (com + escapado ou não) encontra por contactPhone e pelo telefone do solicitante
        for fone in ("%2B5531990000001", "+5531990000001", "5531990000001"):
            r = chamar(self.api, "GET", f"/tickets?phone={fone}&status=open").json
            self.assertEqual([t["number"] for t in r["data"]], [c["number"], a["number"]], fone)
        r = chamar(self.api, "GET", "/tickets?phone=%2B5531990000100").json
        self.assertEqual([t["number"] for t in r["data"]], [b["number"]])
        self.assertEqual(
            chamar(self.api, "GET", "/tickets?phone=%2B5500000000000").json["data"], []
        )
        # status: todos são abertos
        self.assertEqual(chamar(self.api, "GET", "/tickets?status=closed").json["data"], [])
        self.assertEqual(len(chamar(self.api, "GET", "/tickets?status=all").json["data"]), 3)
        self.assert_erro(chamar(self.api, "GET", "/tickets?status=x"), 400, "VALIDATION_ERROR")
        self.assertEqual(
            [
                t["number"]
                for t in chamar(self.api, "GET", f"/tickets?deskId={DESK_FINANCEIRO}").json["data"]
            ],
            [b["number"]],
        )
        # cursor
        p1 = chamar(self.api, "GET", "/tickets?limit=2").json
        self.assertEqual([t["number"] for t in p1["data"]], [c["number"], b["number"]])
        self.assertIsNotNone(p1["nextCursor"])
        p2 = chamar(self.api, "GET", f"/tickets?limit=2&cursor={p1['nextCursor']}").json
        self.assertEqual([t["number"] for t in p2["data"]], [a["number"]])
        self.assertIsNone(p2["nextCursor"])
        self.assert_erro(chamar(self.api, "GET", "/tickets?limit=0"), 400, "VALIDATION_ERROR")
        self.assert_erro(chamar(self.api, "GET", "/tickets?cursor=abc"), 400, "VALIDATION_ERROR")
        # limite acima de 100 é cortado em 100 (e não dá erro)
        self.assertEqual(chamar(self.api, "GET", "/tickets?limit=500").status, 200)

    def test_lookup_do_contato(self) -> None:
        t = chamar(
            self.api,
            "POST",
            "/tickets",
            {"deskId": DESK_N1, "title": "VPN não conecta", "requesterId": REQ_MARIA},
        ).json
        conv = self.cliente_escreve(FONE_MARIA, nome="Maria Souza")["conversationId"]
        r = chamar(self.api, "GET", f"/contacts/lookup?phone={FONE_MARIA.replace('+', '%2B')}")
        self.assertEqual(r.status, 200, r.corpo)
        look = r.json
        self.assertEqual(
            set(look),
            {"phone", "requesters", "clientContacts", "clientIds", "openTickets", "conversation"},
        )
        self.assertEqual(look["phone"], FONE_MARIA)
        self.assertEqual([x["id"] for x in look["requesters"]], [REQ_MARIA])
        self.assertEqual(look["requesters"][0]["client"], {"id": CLIENTE_ACME, "name": "ACME Ltda"})
        self.assertEqual(look["requesters"][0]["clientId"], CLIENTE_ACME)
        self.assertEqual(look["clientContacts"], [])
        self.assertEqual(look["clientIds"], [CLIENTE_ACME])
        self.assertEqual([x["number"] for x in look["openTickets"]], [t["number"]])
        self.assertEqual(look["conversation"]["id"], conv)
        self.assertEqual(look["conversation"]["handledBy"], "EXTERNAL_BOT")
        # telefone desconhecido: tudo vazio, sem conversa
        desconhecido = chamar(self.api, "GET", "/contacts/lookup?phone=%2B5531999999999").json
        self.assertEqual(desconhecido["requesters"], [])
        self.assertEqual(desconhecido["clientIds"], [])
        self.assertEqual(desconhecido["openTickets"], [])
        self.assertIsNone(desconhecido["conversation"])
        self.assert_erro(chamar(self.api, "GET", "/contacts/lookup"), 400, "VALIDATION_ERROR")

    # --- conversas --------------------------------------------------------------

    def test_listar_conversas_com_filtros_e_cursor(self) -> None:
        self.assertEqual(
            chamar(self.api, "GET", "/conversations").json, {"data": [], "nextCursor": None}
        )
        a = self.cliente_escreve("+5531990000001")["conversationId"]
        b = self.cliente_escreve("+5531990000002", nome="João")["conversationId"]
        lista = chamar(self.api, "GET", "/conversations").json["data"]
        self.assertEqual({c["id"] for c in lista}, {a, b})
        item = next(c for c in lista if c["id"] == b)
        self.assertEqual(set(item), CAMPOS_CONVERSA)
        self.assertEqual(item["status"], "PENDING")
        self.assertEqual(item["channel"], "WHATSAPP")
        self.assertEqual(item["handledBy"], "EXTERNAL_BOT")
        self.assertIs(item["botActive"], True)
        self.assertEqual(
            item["contact"],
            {"name": "João", "phone": "+5531990000002", "email": None, "groupJid": None},
        )
        self.assertIsNone(item["departmentId"])
        self.assertIsNone(item["assignedUserId"])
        self.assertTrue(item["lastMessageAt"].endswith("Z"))
        self.assertNotIn("recentMessages", item)

        chamar(self.api, "POST", f"/conversations/{a}/department", {"departmentId": DEP_FINANCEIRO})
        chamar(self.api, "POST", f"/conversations/{a}/assign", {"userId": USER_ANA})

        def ids(query: str) -> list[str]:
            return [c["id"] for c in chamar(self.api, "GET", f"/conversations{query}").json["data"]]

        self.assertEqual(ids(f"?departmentId={DEP_FINANCEIRO}"), [a])
        self.assertEqual(ids("?tab=entrada"), [b])  # sem dono
        self.assertEqual(set(ids("?tab=abertas")), {a, b})
        self.assertEqual(set(ids("?tab=todas")), {a, b})
        self.assertEqual(ids("?tab=encerrados"), [])
        self.assertEqual(ids("?status=IN_PROGRESS"), [a])
        self.assertEqual(ids("?status=PENDING"), [b])
        self.assertEqual(ids(f"?assignedUserId={USER_ANA}"), [a])
        self.assertEqual(ids("?assignedUserId=none"), [b])
        self.assertEqual(ids("?botActive=true"), [b])
        self.assertEqual(ids("?botActive=false"), [a])
        self.assertEqual(ids("?channel=EMAIL"), [])
        self.assertEqual(ids("?phone=%2B5531990000002"), [b])
        for ruim in ("?tab=meus", "?status=OPEN", "?botActive=talvez"):
            self.assert_erro(
                chamar(self.api, "GET", f"/conversations{ruim}"), 400, "VALIDATION_ERROR"
            )
        p1 = chamar(self.api, "GET", "/conversations?limit=1").json
        self.assertEqual(len(p1["data"]), 1)
        self.assertIsNotNone(p1["nextCursor"])
        p2 = chamar(self.api, "GET", f"/conversations?limit=1&cursor={p1['nextCursor']}").json
        self.assertEqual(len(p2["data"]), 1)
        self.assertIsNone(p2["nextCursor"])
        self.assertNotEqual(p1["data"][0]["id"], p2["data"][0]["id"])

    def test_ler_conversa_com_recent_messages_e_janela(self) -> None:
        conv = self.cliente_escreve(texto="Oi")["conversationId"]
        chamar(
            self.api, "POST", f"/conversations/{conv}/messages", {"text": "nota", "internal": True}
        )
        r = chamar(self.api, "GET", f"/conversations/{conv}")
        self.assertEqual(r.status, 200)
        self.assertEqual(set(r.json), CAMPOS_CONVERSA | {"whatsappWindow", "recentMessages"})
        msgs = r.json["recentMessages"]
        self.assertEqual(len(msgs), 1)  # sem notas internas
        self.assertEqual(set(msgs[0]), CAMPOS_MENSAGEM)
        self.assertEqual(msgs[0]["sender"], "CLIENT")
        self.assertEqual(msgs[0]["type"], "TEXT")
        self.assertEqual(msgs[0]["body"], "Oi")
        self.assertEqual(r.json["lastMessagePreview"], "Oi")
        self.assertEqual(r.json["unreadCount"], 1)
        janela = r.json["whatsappWindow"]
        self.assertIs(janela["open"], True)
        self.assertEqual(janela["conversationId"], conv)
        self.assertIsNotNone(janela["expiresAt"])
        self.assert_erro(chamar(self.api, "GET", "/conversations/nao-existe"), 404, "NOT_FOUND")

    def test_enviar_mensagem_usa_text_e_devolve_201_com_messages(self) -> None:
        conv = self.conversa_nova()
        r = chamar(
            self.api,
            "POST",
            f"/conversations/{conv}/messages",
            {"text": "Posso ajudar em algo mais?"},
        )
        self.assertEqual(r.status, 201, r.corpo)
        self.assertEqual(set(r.json), {"conversationId", "messages"})
        self.assertEqual(r.json["conversationId"], conv)
        msg = r.json["messages"][0]
        self.assertEqual(set(msg), CAMPOS_MENSAGEM)
        self.assertEqual(msg["sender"], "AGENT")  # "atendente, bot ou integração" (04)
        self.assertEqual(msg["senderName"], self.mock.config["nome_chave"])
        self.assertEqual(msg["type"], "TEXT")
        self.assertEqual(msg["body"], "Posso ajudar em algo mais?")
        self.assertEqual(msg["status"], "queued")
        self.assertIs(msg["internal"], False)
        self.assertEqual(msg["conversationId"], conv)
        # "mensagem enviada pela integração não tira o bot de cena" (04)
        c = chamar(self.api, "GET", f"/conversations/{conv}").json
        self.assertEqual((c["handledBy"], c["botActive"]), ("EXTERNAL_BOT", True))
        self.assertEqual(c["unreadCount"], 0)
        self.assertEqual([m["sender"] for m in c["recentMessages"]], ["CLIENT", "AGENT"])
        self.assert_erro(
            chamar(self.api, "POST", "/conversations/nao-existe/messages", {"text": "x"}),
            404,
            "NOT_FOUND",
        )
        # A mensagem do bot aparece na UI como bolha do bot.
        estado = chamar(self.base, "GET", "/mock/estado?telefone=%2B5531990000001", chave=None).json
        self.assertEqual([m["sender"] for m in estado["conversa"]["messages"]], ["CLIENT", "AGENT"])

    def test_body_sem_text_e_400_validation_error(self) -> None:
        conv = self.conversa_nova()
        # O guia antigo mandava `body`; na v1 o campo é `text` (docs/infodesk/04).
        erro = self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/messages", {"body": "Olá"}),
            400,
            "VALIDATION_ERROR",
        )
        self.assertEqual(erro["errors"][0]["field"], "text")
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/messages", {}), 400, "VALIDATION_ERROR"
        )
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/messages", {"text": "  "}),
            400,
            "VALIDATION_ERROR",
        )
        self.assert_erro(
            chamar(
                self.api,
                "POST",
                f"/conversations/{conv}/messages",
                {"text": "x", "internal": "sim"},
            ),
            400,
            "VALIDATION_ERROR",
        )
        # com `text` e `body` juntos, vale o `text`
        r = chamar(self.api, "POST", f"/conversations/{conv}/messages", {"text": "ok", "body": "x"})
        self.assertEqual(r.status, 201)
        self.assertEqual(r.json["messages"][0]["body"], "ok")
        # nada foi gravado pelas tentativas inválidas
        msgs = chamar(self.api, "GET", f"/conversations/{conv}").json["recentMessages"]
        self.assertEqual([m["body"] for m in msgs], ["Bom dia, preciso de ajuda", "ok"])

    def test_mensagem_interna_e_nota_so_da_equipe(self) -> None:
        conv = self.conversa_nova()
        r = chamar(
            self.api,
            "POST",
            f"/conversations/{conv}/messages",
            {"text": "Cliente confirmou que reiniciou o roteador.", "internal": True},
        )
        self.assertEqual(r.status, 201, r.corpo)
        self.assertIs(r.json["messages"][0]["internal"], True)
        c = chamar(self.api, "GET", f"/conversations/{conv}").json
        self.assertEqual([m["sender"] for m in c["recentMessages"]], ["CLIENT"])  # sem a nota
        self.assertEqual(c["lastMessagePreview"], "Bom dia, preciso de ajuda")
        # a UI do mock mostra a nota
        estado = chamar(self.base, "GET", "/mock/estado?telefone=%2B5531990000001", chave=None).json
        self.assertEqual([m["internal"] for m in estado["conversa"]["messages"]], [False, True])

    def test_department_null_remove(self) -> None:
        conv = self.cliente_escreve()["conversationId"]
        r = chamar(
            self.api, "POST", f"/conversations/{conv}/department", {"departmentId": DEP_FINANCEIRO}
        )
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["departmentId"], DEP_FINANCEIRO)
        self.assertEqual(r.json["handledBy"], "EXTERNAL_BOT")  # mudar de fila não solta o bot (04)
        r = chamar(self.api, "POST", f"/conversations/{conv}/department", {"departmentId": None})
        self.assertEqual(r.status, 200, r.corpo)
        self.assertIsNone(r.json["departmentId"])
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/department", {}),
            400,
            "VALIDATION_ERROR",
        )
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/department", {"departmentId": "x"}),
            400,
            "INVALID_REFERENCE",
        )

    def test_assign_emite_conversation_assigned_e_tira_o_bot(self) -> None:
        conv = self.conversa_nova()
        r = chamar(self.api, "POST", f"/conversations/{conv}/assign", {"userId": USER_ANA})
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["assignedUserId"], USER_ANA)
        self.assertEqual(r.json["handledBy"], "AGENT")
        self.assertIs(r.json["botActive"], False)
        self.assertEqual(r.json["status"], "IN_PROGRESS")
        (e,) = self.gateway.esperar(1)
        self.assertEqual(e["event"], "conversation.assigned")
        self.assertEqual(set(e["json"]["data"]), {"conversation", "contact"})  # extras: 07 item W7
        self.assertEqual(e["json"]["data"]["conversation"]["handledBy"], "AGENT")
        self.assertEqual(e["json"]["data"]["conversation"]["assignedUserId"], USER_ANA)
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/assign", {"userId": "x"}),
            400,
            "INVALID_REFERENCE",
        )
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/assign", {}), 400, "VALIDATION_ERROR"
        )

    def test_handoff_para_a_fila(self) -> None:
        conv = self.conversa_nova()
        nota = "Cliente Maria (ACME). Impressora sem imprimir. Chamado #1001 aberto."
        r = chamar(
            self.api,
            "POST",
            f"/conversations/{conv}/handoff",
            {"departmentId": DEP_FINANCEIRO, "note": nota},
        )
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["handledBy"], "QUEUE")
        self.assertIs(r.json["botActive"], False)
        self.assertEqual(r.json["status"], "PENDING")
        self.assertEqual(r.json["departmentId"], DEP_FINANCEIRO)
        self.assertIsNone(r.json["assignedUserId"])
        (e,) = self.gateway.esperar(1)
        self.assertEqual(e["event"], "conversation.handoff")
        self.assertEqual(e["json"]["data"]["note"], nota)  # extra `note` (05)
        self.assertEqual(e["json"]["data"]["conversation"]["handledBy"], "QUEUE")
        self.assertIs(e["json"]["data"]["conversation"]["botActive"], False)
        # a `note` vira nota interna: aparece na UI, não no histórico público
        estado = chamar(self.base, "GET", "/mock/estado?telefone=%2B5531990000001", chave=None).json
        internas = [m for m in estado["conversa"]["messages"] if m["internal"]]
        self.assertEqual([m["body"] for m in internas], [nota])
        recentes = chamar(self.api, "GET", f"/conversations/{conv}").json["recentMessages"]
        self.assertEqual([m["sender"] for m in recentes], ["CLIENT"])
        # a partir daqui o evento de mensagem do cliente já vem com handledBy != EXTERNAL_BOT
        self.gateway.limpar()
        self.cliente_escreve(texto="alguém aí?")
        (recebida,) = self.gateway.esperar(1)
        self.assertEqual(recebida["event"], "message.received")
        self.assertEqual(recebida["json"]["data"]["conversation"]["handledBy"], "QUEUE")

    def test_handoff_para_um_atendente(self) -> None:
        conv = self.conversa_nova()
        r = chamar(
            self.api,
            "POST",
            f"/conversations/{conv}/handoff",
            {"userId": USER_ANA, "note": "resumo"},
        )
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["handledBy"], "AGENT")
        self.assertEqual(r.json["assignedUserId"], USER_ANA)
        self.assertIs(r.json["botActive"], False)
        self.assertEqual(r.json["status"], "IN_PROGRESS")
        self.assertEqual([e["event"] for e in self.gateway.esperar(1)], ["conversation.handoff"])

    def test_handoff_valida_e_nao_exige_nota(self) -> None:
        conv = self.conversa_nova()
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/handoff", {"departmentId": "x"}),
            400,
            "INVALID_REFERENCE",
        )
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/handoff", {"userId": "x"}),
            400,
            "INVALID_REFERENCE",
        )
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/handoff", {"note": 5}),
            400,
            "VALIDATION_ERROR",
        )
        self.assert_erro(
            chamar(self.api, "POST", "/conversations/nao-existe/handoff", {}), 404, "NOT_FOUND"
        )
        # 07/08: se `note` é obrigatória é "não dito" → o mock aceita sem
        r = chamar(self.api, "POST", f"/conversations/{conv}/handoff", {})
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["handledBy"], "QUEUE")

    def test_vincular_solicitante_a_conversa(self) -> None:
        conv = self.conversa_nova()
        r = chamar(self.api, "POST", f"/conversations/{conv}/requester", {"id": REQ_MARIA})
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["requesterId"], REQ_MARIA)
        self.assertEqual(r.json["clientId"], CLIENTE_ACME)  # o cliente vem junto
        r = chamar(self.api, "POST", f"/conversations/{conv}/requester", {"id": None})
        self.assertEqual(r.status, 200, r.corpo)
        self.assertIsNone(r.json["requesterId"])
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/requester", {"id": "x"}),
            400,
            "INVALID_REFERENCE",
        )
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/requester", {}),
            400,
            "VALIDATION_ERROR",
        )
        self.assert_erro(
            chamar(self.api, "POST", "/conversations/nao-existe/requester", {"id": None}),
            404,
            "NOT_FOUND",
        )

    def test_abrir_ticket_a_partir_da_conversa(self) -> None:
        conv = self.conversa_nova()
        chamar(self.api, "POST", f"/conversations/{conv}/requester", {"id": REQ_MARIA})
        chamar(
            self.api,
            "POST",
            f"/conversations/{conv}/messages",
            {"text": "Qual é o modelo da impressora?"},
        )
        prio = self.prioridade(DESK_N1, "Alta")
        r = chamar(
            self.api,
            "POST",
            f"/conversations/{conv}/ticket",
            {
                "deskId": DESK_N1,
                "title": "Impressora do financeiro não imprime",
                "description": "Resumo da triagem: HP M428, erro 49.",
                "priorityId": prio,
                "period": "session",
                "finish": False,
            },
        )
        self.assertEqual(r.status, 201, r.corpo)
        t = r.json
        self.assertEqual(set(t), CAMPOS_TICKET)
        self.assertEqual(t["conversationId"], conv)  # chamado já nasce vinculado (03)
        self.assertEqual(t["channel"], "WHATSAPP")
        self.assertEqual(t["priority"]["id"], prio)
        self.assertEqual(t["requester"]["id"], REQ_MARIA)  # herda da conversa (07 item T11)
        self.assertEqual(t["client"]["id"], CLIENTE_ACME)
        self.assertEqual(t["contactPhone"], "+5531990000001")
        # transcrição abaixo do resumo
        self.assertTrue(t["description"].startswith("Resumo da triagem: HP M428, erro 49."))
        self.assertIn("Cliente: Bom dia, preciso de ajuda", t["description"])
        self.assertIn(
            f"{self.mock.config['nome_chave']}: Qual é o modelo da impressora?", t["description"]
        )
        c = chamar(self.api, "GET", f"/conversations/{conv}").json
        self.assertEqual(c["ticketNumber"], t["number"])
        self.assertEqual(c["status"], "PENDING")  # finish:false mantém aberta
        self.assertEqual(self.gateway.esperar(1, timeout=0.3), [])
        # lookup enxerga o chamado aberto do solicitante
        self.assertEqual(
            [
                x["number"]
                for x in chamar(self.api, "GET", "/tickets?phone=%2B5531990000001").json["data"]
            ],
            [t["number"]],
        )
        # periods: today/24h/all/session aceitos; outro valor → 400
        for periodo in ("today", "24h", "all"):
            ok = chamar(
                self.api,
                "POST",
                f"/conversations/{conv}/ticket",
                {"deskId": DESK_N1, "title": periodo, "period": periodo},
            )
            self.assertEqual(ok.status, 201, ok.corpo)
            self.assertIn("Cliente: Bom dia", ok.json["description"])
        self.assert_erro(
            chamar(
                self.api,
                "POST",
                f"/conversations/{conv}/ticket",
                {"deskId": DESK_N1, "title": "x", "period": "ontem"},
            ),
            400,
            "VALIDATION_ERROR",
        )
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/ticket", {"title": "x"}),
            400,
            "VALIDATION_ERROR",
        )
        self.assert_erro(
            chamar(
                self.api, "POST", f"/conversations/{conv}/ticket", {"deskId": "x", "title": "x"}
            ),
            400,
            "INVALID_REFERENCE",
        )
        self.assert_erro(
            chamar(
                self.api,
                "POST",
                f"/conversations/{conv}/ticket",
                {"deskId": DESK_N1, "title": "x", "finish": "sim"},
            ),
            400,
            "VALIDATION_ERROR",
        )
        self.assert_erro(
            chamar(
                self.api,
                "POST",
                "/conversations/nao-existe/ticket",
                {"deskId": DESK_N1, "title": "x"},
            ),
            404,
            "NOT_FOUND",
        )

    def test_abrir_ticket_da_conversa_com_finish_encerra(self) -> None:
        conv = self.conversa_nova()
        r = chamar(
            self.api,
            "POST",
            f"/conversations/{conv}/ticket",
            {"deskId": DESK_N1, "title": "x", "finish": True},
        )
        self.assertEqual(r.status, 201, r.corpo)
        c = chamar(self.api, "GET", f"/conversations/{conv}").json
        self.assertEqual(c["status"], "FINISHED")
        self.assertEqual(c["ticketNumber"], r.json["number"])
        self.assertEqual([e["event"] for e in self.gateway.esperar(1)], ["conversation.finished"])

    def test_link_ticket_a_chamado_existente(self) -> None:
        t = chamar(
            self.api, "POST", "/tickets", {"deskId": DESK_N1, "title": "VPN não conecta"}
        ).json
        conv = self.conversa_nova()
        r = chamar(
            self.api,
            "POST",
            f"/conversations/{conv}/link-ticket",
            {"ticketNumber": t["number"], "finish": False},
        )
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["ticketNumber"], t["number"])
        self.assertEqual(r.json["status"], "PENDING")
        g = chamar(self.api, "GET", f"/tickets/{t['number']}").json
        self.assertEqual(g["conversationId"], conv)
        # a transcrição da sessão foi anexada (nota interna); o fluxo de 08 segue com /notes
        self.assertIn("Cliente: Bom dia", self.mock.tickets[t["number"]]["notes"][0]["body"])
        self.assertEqual(
            chamar(
                self.api,
                "POST",
                f"/tickets/{t['number']}/notes",
                {"body": "Cliente informou que..."},
            ).status,
            201,
        )
        # mesma conversa de novo: ok; outra conversa: "chamado preso a outro atendimento" (06)
        self.assertEqual(
            chamar(
                self.api,
                "POST",
                f"/conversations/{conv}/link-ticket",
                {"ticketNumber": t["number"]},
            ).status,
            200,
        )
        outra = self.conversa_nova("+5531990000002")
        self.assert_erro(
            chamar(
                self.api,
                "POST",
                f"/conversations/{outra}/link-ticket",
                {"ticketNumber": t["number"]},
            ),
            409,
            "CONFLICT",
        )
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{outra}/link-ticket", {"ticketNumber": 9999}),
            400,
            "INVALID_REFERENCE",
        )
        for ruim in ({}, {"ticketNumber": "1001"}, {"ticketNumber": True}):
            self.assert_erro(
                chamar(self.api, "POST", f"/conversations/{outra}/link-ticket", ruim),
                400,
                "VALIDATION_ERROR",
            )

    def test_link_ticket_com_finish_encerra(self) -> None:
        t = chamar(self.api, "POST", "/tickets", {"deskId": DESK_N1, "title": "x"}).json
        conv = self.conversa_nova()
        r = chamar(
            self.api,
            "POST",
            f"/conversations/{conv}/link-ticket",
            {"ticketNumber": t["number"], "finish": True},
        )
        self.assertEqual(r.json["status"], "FINISHED")
        self.assertEqual([e["event"] for e in self.gateway.esperar(1)], ["conversation.finished"])

    def test_finish_encerra_emite_webhook_e_bloqueia_mensagens(self) -> None:
        conv = self.conversa_nova()
        r = chamar(self.api, "POST", f"/conversations/{conv}/finish")
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["status"], "FINISHED")
        self.assertEqual(set(r.json), CAMPOS_CONVERSA)
        entregas = self.gateway.esperar(1)
        self.assertEqual([e["event"] for e in entregas], ["conversation.finished"])
        data = entregas[0]["json"]["data"]
        self.assertEqual(data["conversation"]["id"], conv)
        self.assertEqual(data["conversation"]["status"], "FINISHED")
        self.assertEqual(data["contact"]["phone"], "+5531990000001")
        self.assertNotIn("reason", data)  # ausente = atendente/API (05)
        # idempotente: segundo finish não emite de novo
        self.assertEqual(chamar(self.api, "POST", f"/conversations/{conv}/finish").status, 200)
        self.assertEqual(len(self.gateway.esperar(2, timeout=0.3)), 1)
        self.assertEqual(
            [
                c["id"]
                for c in chamar(self.api, "GET", "/conversations?tab=encerrados").json["data"]
            ],
            [conv],
        )
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/messages", {"text": "x"}),
            404,
            "NOT_FOUND",
        )
        # Próxima mensagem do mesmo telefone abre OUTRA conversa (07 item C5).
        self.assertNotEqual(self.cliente_escreve()["conversationId"], conv)

    # --- webhooks ------------------------------------------------------------

    def test_webhook_assinado_com_timestamp(self) -> None:
        self.cliente_escreve()
        entregas = self.gateway.esperar(2)
        self.assertEqual(len(entregas), 2)
        deliveries = set()
        for e in entregas:
            self.assertEqual(e["path"], "/webhook")
            self.assertEqual(e["content_type"], "application/json")
            # X-Infominas-Timestamp: segundos Unix, dentro da janela de 5 min (05)
            self.assertRegex(e["timestamp"], r"^\d{10}$")
            self.assertLess(abs(time.time() - int(e["timestamp"])), 300)
            # assinatura = HMAC-SHA256(secret, "<ts>.<corpo bruto>")
            self.assertEqual(e["signature"], assinatura_esperada(e["timestamp"], e["raw"]))
            self.assertTrue(e["signature"].startswith("sha256="))
            # a assinatura antiga (só o corpo) NÃO vale mais
            antiga = "sha256=" + hmac.new(SEGREDO.encode(), e["raw"], hashlib.sha256).hexdigest()
            self.assertNotEqual(e["signature"], antiga)
            # outro timestamp invalida
            self.assertNotEqual(
                e["signature"], assinatura_esperada(str(int(e["timestamp"]) + 1), e["raw"])
            )
            self.assertTrue(e["delivery"])
            deliveries.add(e["delivery"])
            self.assertEqual(e["json"]["event"], e["event"])
        self.assertEqual(len(deliveries), 2)  # id de ENTREGA único por tentativa

    def test_envelope_e_data_completos_como_na_doc_05(self) -> None:
        res = self.cliente_escreve(texto="Bom dia, minha impressora parou")
        entregas = self.gateway.esperar(2)
        self.assertEqual(
            [e["event"] for e in entregas], ["conversation.started", "message.received"]
        )
        for e in entregas:
            env = e["json"]
            self.assertEqual(
                set(env), {"id", "seq", "event", "createdAt", "organizationId", "data"}
            )
            self.assertRegex(env["id"], r"^evt_\d{12}$")
            self.assertIsInstance(env["seq"], int)
            self.assertEqual(env["organizationId"], self.mock.config["org_id"])
            self.assertRegex(env["createdAt"], r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$")
            conv = env["data"]["conversation"]
            self.assertEqual(set(conv), CAMPOS_CONVERSA)
            self.assertEqual(conv["id"], res["conversationId"])
            self.assertEqual(conv["channel"], "WHATSAPP")
            self.assertEqual(conv["handledBy"], "EXTERNAL_BOT")
            self.assertIs(conv["botActive"], True)
            self.assertEqual(conv["status"], "PENDING")
            self.assertIsNone(conv["ticketNumber"])
            self.assertIsNotNone(conv["sessionStartedAt"])
            self.assertEqual(
                conv["contact"],
                {"name": "Maria", "phone": "+5531990000001", "email": None, "groupJid": None},
            )
            self.assertEqual(env["data"]["contact"], conv["contact"])
        iniciada, recebida = (e["json"] for e in entregas)
        self.assertEqual(iniciada["seq"] + 1, recebida["seq"])
        self.assertNotEqual(iniciada["id"], recebida["id"])
        # started não leva `message`; message.received leva
        self.assertEqual(set(iniciada["data"]), {"conversation", "contact"})
        self.assertEqual(set(recebida["data"]), {"conversation", "contact", "message"})
        msg = recebida["data"]["message"]
        self.assertEqual(set(msg), CAMPOS_MENSAGEM)
        self.assertEqual(msg["id"], res["messageId"])
        self.assertEqual(msg["conversationId"], res["conversationId"])
        self.assertEqual(msg["sender"], "CLIENT")
        self.assertIsNone(msg["senderName"])
        self.assertEqual(msg["type"], "TEXT")
        self.assertEqual(msg["body"], "Bom dia, minha impressora parou")
        self.assertIs(msg["internal"], False)
        self.assertIs(msg["deleted"], False)
        self.assertIsNone(msg["attachment"])
        self.assertIsNone(msg["replyToId"])
        self.assertEqual(recebida["data"]["conversation"]["lastMessagePreview"], msg["body"])
        self.assertTrue(all(x["ok"] for x in res["entregas"]))
        # Segunda mensagem do mesmo telefone: só message.received, mesma conversa, seq maior.
        self.gateway.limpar()
        res2 = self.cliente_escreve(texto="Ainda estou esperando")
        self.assertEqual(res2["conversationId"], res["conversationId"])
        (e2,) = self.gateway.esperar(1)
        self.assertEqual(e2["event"], "message.received")
        self.assertGreater(e2["json"]["seq"], recebida["seq"])

    def test_audio_e_message_received_com_type_audio_e_body_vazio(self) -> None:
        r = chamar(
            self.base,
            "POST",
            "/mock/cliente/audio",
            {"telefone": "+5531990000009", "nome": "Zé"},
            chave=None,
        )
        self.assertEqual(r.status, 201, r.corpo)
        entregas = self.gateway.esperar(2)
        msg = entregas[1]["json"]["data"]["message"]
        self.assertEqual(msg["body"], "")
        self.assertEqual(msg["sender"], "CLIENT")
        self.assertEqual(msg["type"], "AUDIO")
        self.assertTrue(
            msg["attachment"]["url"].startswith("https://")
        )  # só `url` é documentado (C9)

    def test_falha_de_entrega_fica_registrada_sem_reentrega_automatica(self) -> None:
        self.mock.config["sem_reentrega"] = True
        self.gateway.status = 500
        res = self.cliente_escreve()
        self.assertEqual(len(self.gateway.esperar(2)), 2)  # tentou uma vez cada
        self.assertTrue(all(not x["ok"] and x["status"] == 500 for x in res["entregas"]))
        self.gateway.status = 200
        self.mock.config["reentrega_atrasos_s"] = [
            0.05
        ]  # mesmo com atraso curto: MOCK_SEM_REENTREGA manda
        time.sleep(0.2)
        self.assertEqual(len(self.gateway.esperar(3, timeout=0.3)), 2)  # nenhuma reentrega
        estado = chamar(self.base, "GET", "/mock/estado?telefone=%2B5531990000001", chave=None).json
        self.assertTrue(any("falhou" in e["texto"] for e in estado["conversa"]["eventos"]))

    def test_reentrega_automatica_mantem_o_id_do_evento_e_troca_o_delivery(self) -> None:
        self.mock.config.update(sem_reentrega=False, reentrega_atrasos_s=[0.05])
        self.gateway.status = 500
        self.cliente_escreve()
        entregas = self.gateway.esperar(4)  # 2 eventos × (tentativa inicial + 1 reentrega)
        self.assertEqual(len(entregas), 4)
        por_evento: dict[str, list[dict[str, Any]]] = {}
        for e in entregas:
            por_evento.setdefault(e["json"]["id"], []).append(e)
        self.assertEqual(len(por_evento), 2)
        for tentativas in por_evento.values():
            self.assertEqual(len(tentativas), 2)
            a, b = tentativas
            self.assertEqual(a["json"], b["json"])  # mesmo envelope (id, seq, data)
            self.assertNotEqual(a["delivery"], b["delivery"])
            # cada reentrega é assinada com o SEU timestamp
            self.assertEqual(b["signature"], assinatura_esperada(b["timestamp"], b["raw"]))
        # o registro da tentativa é gravado depois da resposta do gateway: espera assentar
        limite = time.time() + 3
        while len(self.mock.entregas) < 4 and time.time() < limite:
            time.sleep(0.02)
        tentativas_registradas = sorted(x["tentativa"] for x in self.mock.entregas)
        self.assertEqual(tentativas_registradas, [1, 1, 2, 2])

    def test_gateway_fora_do_ar_nao_derruba_o_mock(self) -> None:
        self.mock.config["gateway_webhook_url"] = "http://127.0.0.1:1/webhook"
        try:
            res = self.cliente_escreve()
            self.assertFalse(res["entregas"][0]["ok"])
            self.assertIn("sem resposta do gateway", res["entregas"][0]["erro"])
        finally:
            self.mock.config["gateway_webhook_url"] = self.gateway.url

    # --- feed de eventos ---------------------------------------------------------

    def test_events_pagina_por_seq_com_o_mesmo_envelope_do_webhook(self) -> None:
        for n in range(3):
            self.cliente_escreve(f"+553199000010{n}", nome=f"Cliente {n}")
        webhooks = self.gateway.esperar(6)
        self.assertEqual(len(webhooks), 6)
        p1 = chamar(self.api, "GET", "/events?after=0&limit=4")
        self.assertEqual(p1.status, 200, p1.corpo)
        self.assertEqual(set(p1.json), {"data", "nextAfter", "hasMore"})
        self.assertEqual(len(p1.json["data"]), 4)
        self.assertIs(p1.json["hasMore"], True)
        seqs = [e["seq"] for e in p1.json["data"]]
        self.assertEqual(seqs, sorted(seqs))
        self.assertEqual(p1.json["nextAfter"], seqs[-1])
        # o feed entrega o MESMO envelope do webhook ("o mesmo handler serve para os dois")
        por_id = {w["json"]["id"]: w["json"] for w in webhooks}
        for env in p1.json["data"]:
            self.assertEqual(env, por_id[env["id"]])
        p2 = chamar(self.api, "GET", f"/events?after={p1.json['nextAfter']}&limit=4").json
        self.assertEqual(len(p2["data"]), 2)
        self.assertIs(p2["hasMore"], False)
        self.assertEqual(p2["nextAfter"], p2["data"][-1]["seq"])
        self.assertEqual(
            [e["seq"] for e in p1.json["data"] + p2["data"]], [w["json"]["seq"] for w in webhooks]
        )
        # sem eventos novos: nextAfter fica onde estava
        vazio = chamar(self.api, "GET", f"/events?after={p2['nextAfter']}").json
        self.assertEqual(vazio, {"data": [], "nextAfter": p2["nextAfter"], "hasMore": False})
        # filtros
        so_started = chamar(self.api, "GET", "/events?types=conversation.started").json["data"]
        self.assertEqual({e["event"] for e in so_started}, {"conversation.started"})
        self.assertEqual(len(so_started), 3)
        conv = p1.json["data"][0]["data"]["conversation"]["id"]
        da_conversa = chamar(self.api, "GET", f"/events?conversationId={conv}").json["data"]
        self.assertEqual(len(da_conversa), 2)
        self.assert_erro(chamar(self.api, "GET", "/events?after=x"), 400, "VALIDATION_ERROR")
        self.assert_erro(chamar(self.api, "GET", "/events?after=-1"), 400, "VALIDATION_ERROR")
        self.assert_erro(chamar(self.api, "GET", "/events?limit=0"), 400, "VALIDATION_ERROR")

    def test_events_registra_assigned_handoff_e_finished_e_o_seq_nao_regride_no_reset(self) -> None:
        conv = self.conversa_nova()
        chamar(self.api, "POST", f"/conversations/{conv}/assign", {"userId": USER_ANA})
        chamar(self.api, "POST", f"/conversations/{conv}/handoff", {"note": "n"})
        chamar(self.api, "POST", f"/conversations/{conv}/finish")
        feed = chamar(self.api, "GET", "/events").json["data"]
        self.assertEqual(
            [e["event"] for e in feed],
            [
                "conversation.started",
                "message.received",
                "conversation.assigned",
                "conversation.handoff",
                "conversation.finished",
            ],
        )
        ultimo = feed[-1]["seq"]
        self.mock.reset()
        self.assertEqual(chamar(self.api, "GET", "/events").json["data"], [])
        self.cliente_escreve()
        novo = chamar(self.api, "GET", "/events").json["data"][0]
        self.assertGreater(novo["seq"], ultimo)  # `seq` só cresce (05)

    # --- UI / controle -----------------------------------------------------------

    def test_ui_atendente_assume_emite_assigned_e_tira_o_bot(self) -> None:
        conv = self.conversa_nova()
        r = chamar(
            self.base, "POST", "/mock/atendente/assume", {"telefone": "+5531990000001"}, chave=None
        )
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["handledBy"], "AGENT")
        (e,) = self.gateway.esperar(1)
        self.assertEqual(e["event"], "conversation.assigned")
        conversa = e["json"]["data"]["conversation"]
        self.assertEqual(conversa["id"], conv)
        self.assertEqual(conversa["handledBy"], "AGENT")
        self.assertIs(conversa["botActive"], False)
        self.assertEqual(conversa["assignedUserId"], USER_ANA)  # primeiro atendente de /users
        api = chamar(self.api, "GET", f"/conversations/{conv}").json
        self.assertEqual((api["handledBy"], api["botActive"]), ("AGENT", False))
        # o cliente continua escrevendo: o webhook já diz que não é mais a vez do bot
        self.gateway.limpar()
        self.cliente_escreve(texto="obrigado")
        (m,) = self.gateway.esperar(1)
        self.assertEqual(m["event"], "message.received")
        self.assertEqual(m["json"]["data"]["conversation"]["handledBy"], "AGENT")
        self.assertIs(m["json"]["data"]["conversation"]["botActive"], False)
        # atendente específico; telefone sem conversa → 404
        self.assert_erro(
            chamar(
                self.base,
                "POST",
                "/mock/atendente/assume",
                {"telefone": "+5500000000000"},
                chave=None,
            ),
            404,
            "NOT_FOUND",
        )
        self.assert_erro(
            chamar(
                self.base,
                "POST",
                "/mock/atendente/assume",
                {"telefone": "+5531990000001", "userId": "x"},
                chave=None,
            ),
            400,
            "INVALID_REFERENCE",
        )

    def test_ui_reentregar_ultimo_evento_mantem_id_e_troca_delivery(self) -> None:
        self.assert_erro(
            chamar(self.base, "POST", "/mock/reentregar", {}, chave=None), 404, "NOT_FOUND"
        )
        self.cliente_escreve()
        original = self.gateway.esperar(2)[1]
        self.assertEqual(original["event"], "message.received")
        self.gateway.limpar()
        time.sleep(0.01)
        r = chamar(
            self.base, "POST", "/mock/reentregar", {"telefone": "+5531990000001"}, chave=None
        )
        self.assertEqual(r.status, 200, r.corpo)
        self.assertIs(r.json["ok"], True)
        (de_novo,) = self.gateway.esperar(1)
        self.assertEqual(de_novo["event"], "message.received")
        self.assertEqual(de_novo["json"]["id"], original["json"]["id"])  # dedup pelo `id` do evento
        self.assertEqual(de_novo["json"]["seq"], original["json"]["seq"])
        self.assertEqual(de_novo["json"], original["json"])
        self.assertNotEqual(de_novo["delivery"], original["delivery"])
        self.assertEqual(
            de_novo["signature"], assinatura_esperada(de_novo["timestamp"], de_novo["raw"])
        )
        # sem filtro de telefone reenvia o último evento de qualquer conversa
        self.gateway.limpar()
        chamar(self.base, "POST", "/mock/reentregar", {}, chave=None)
        self.assertEqual(self.gateway.esperar(1)[0]["json"]["id"], original["json"]["id"])
        self.assert_erro(
            chamar(
                self.base, "POST", "/mock/reentregar", {"telefone": "+5500000000000"}, chave=None
            ),
            404,
            "NOT_FOUND",
        )

    def test_ui_e_estado(self) -> None:
        r = chamar(self.base, "GET", "/", chave=None)
        self.assertEqual(r.status, 200)
        self.assertIn("text/html", r.headers.get("Content-Type"))
        html = r.corpo.decode("utf-8")
        self.assertIn("Mock Infodesk", html)
        self.assertIn("Atendente assume", html)
        self.assertIn("Reentregar último evento", html)
        self.cliente_escreve()
        conv = chamar(self.api, "GET", "/conversations").json["data"][0]["id"]
        chamar(
            self.api, "POST", f"/conversations/{conv}/department", {"departmentId": DEP_FINANCEIRO}
        )
        chamar(
            self.api,
            "POST",
            "/tickets",
            {"deskId": DESK_N1, "title": "Sem internet", "contactPhone": "+5531990000001"},
        )
        estado = chamar(self.base, "GET", "/mock/estado?telefone=%2B5531990000001", chave=None).json
        self.assertEqual(estado["conversa"]["id"], conv)
        self.assertEqual(estado["conversa"]["handledBy"], "EXTERNAL_BOT")
        self.assertEqual(estado["conversa"]["departmentName"], "Financeiro")
        self.assertEqual(estado["tickets"][0]["title"], "Sem internet")
        self.assertEqual(estado["tickets"][0]["deskName"], "Suporte Tecnico N1")
        self.assertEqual(estado["tickets"][0]["status"], "Aberto")
        self.assertIs(type(estado["tickets"][0]["number"]), int)
        self.assertEqual(
            [e["event"] for e in estado["entregas"]], ["conversation.started", "message.received"]
        )
        self.assertEqual(estado["config"]["semReentrega"], True)
        # encerrar pela UI emite conversation.finished
        self.gateway.limpar()
        r = chamar(
            self.base, "POST", "/mock/cliente/encerrar", {"telefone": "+5531990000001"}, chave=None
        )
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual([e["event"] for e in self.gateway.esperar(1)], ["conversation.finished"])
        self.assert_erro(
            chamar(
                self.base,
                "POST",
                "/mock/cliente/encerrar",
                {"telefone": "+5531990000001"},
                chave=None,
            ),
            404,
            "NOT_FOUND",
        )

    def test_log_nao_expoe_telefone_nem_query_string(self) -> None:
        linhas: list[str] = []
        self.mock.log = linhas.append
        try:
            self.cliente_escreve("+5531998877665", texto="oi")
            chamar(self.base, "GET", "/mock/estado?telefone=%2B5531998877665", chave=None)
            chamar(self.api, "GET", "/conversations?departmentId=5531998877665")
            chamar(self.api, "GET", "/contacts/lookup?phone=%2B5531998877665")
            chamar(self.api, "GET", "/tickets?phone=%2B5531998877665")
        finally:
            self.mock.log = lambda *_a, **_k: None
        texto = "\n".join(linhas)
        self.assertIn("[mock] GET /mock/estado → 200", texto)
        self.assertIn("[mock] GET /api/integration/v1/contacts/lookup → 200", texto)
        self.assertNotIn("telefone=", texto)
        self.assertNotIn("phone=", texto)
        self.assertNotRegex(texto, r"\d{8,}")
        self.assertIn("***7665", texto)

    def test_content_length_acima_do_teto_fecha_a_conexao_keep_alive(self) -> None:
        # Socket cru, mesma conexão: POST com Content-Length acima do teto (corpo pequeno)
        # seguido de um GET. O mock responde 400 UMA vez, com Connection: close, e fecha:
        # os bytes do corpo não viram a "próxima requisição" (nada de 501 do http.server).
        pedido = (
            "POST /api/integration/v1/tickets HTTP/1.1\r\nHost: x\r\n"
            f"Authorization: Bearer {CHAVE}\r\nContent-Type: application/json\r\n"
            f"Content-Length: {mock_infodesk.CORPO_MAX_BYTES + 1}\r\n\r\n"
            '{"deskId":"x","title":"y"}'
            f"GET /api/integration/v1/desks HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer {CHAVE}\r\n\r\n"
        ).encode()
        with socket.create_connection(
            ("127.0.0.1", self.servidor.server_address[1]), timeout=5
        ) as sock:
            sock.sendall(pedido)
            recebido = b""
            while True:
                parte = sock.recv(65536)
                if not parte:
                    break
                recebido += parte
        texto = recebido.decode("utf-8")
        self.assertTrue(texto.startswith("HTTP/1.1 400 "), texto[:80])
        self.assertEqual(texto.count("HTTP/1.1 "), 1, texto)
        self.assertIn("Connection: close", texto)
        self.assertNotIn("501", texto)
        corpo = json.loads(texto.split("\r\n\r\n", 1)[1])
        self.assertEqual(corpo["statusCode"], 400)
        self.assertEqual(corpo["code"], "VALIDATION_ERROR")

    def test_metodo_errado_em_rota_conhecida_vira_404_not_found(self) -> None:
        # O PDF não cita 405 nem dá `code` para ele (06): o mock devolve NOT_FOUND.
        self.assert_erro(chamar(self.api, "POST", "/desks", {}), 404, "NOT_FOUND")
        self.assert_erro(chamar(self.api, "GET", "/conversations/x/finish"), 404, "NOT_FOUND")
        self.assert_erro(chamar(self.api, "POST", "/me", {}), 404, "NOT_FOUND")
        # mesma rota com métodos diferentes (GET/POST /tickets) continua funcionando
        self.assertEqual(chamar(self.api, "GET", "/tickets").status, 200)
        self.assertEqual(
            chamar(self.api, "POST", "/tickets", {"deskId": DESK_N1, "title": "x"}).status, 201
        )

    def test_referencia_exige_escopo_quando_configurado(self) -> None:
        self.mock.config["referencia_exige_escopo"] = True
        try:
            self.mock.config["escopos"] = ["tickets:read"]
            self.assertEqual(chamar(self.api, "GET", "/desks").status, 200)
            self.assertEqual(chamar(self.api, "GET", f"/desks/{DESK_N1}").status, 200)
            self.assertEqual(chamar(self.api, "GET", f"/catalog?deskId={DESK_N1}").status, 200)
            self.assert_erro(chamar(self.api, "GET", "/users"), 403, "FORBIDDEN")
            self.assert_erro(chamar(self.api, "GET", "/departments"), 403, "FORBIDDEN")
            self.assertEqual(chamar(self.api, "GET", "/me").status, 200)  # /me nunca exige
        finally:
            self.mock.config["referencia_exige_escopo"] = False

    def test_finish_sem_webhook_quando_configurado(self) -> None:
        conv = self.conversa_nova()
        self.mock.config["finish_emite_webhook"] = False
        try:
            self.assertEqual(
                chamar(self.api, "POST", f"/conversations/{conv}/finish").json["status"], "FINISHED"
            )
            self.assertEqual(self.gateway.esperar(1, timeout=0.3), [])
            self.assertNotIn(
                "conversation.finished",
                [e["event"] for e in chamar(self.api, "GET", "/events").json["data"]],
            )
        finally:
            self.mock.config["finish_emite_webhook"] = True

    def test_mock_config_nao_expoe_segredos(self) -> None:
        cfg = chamar(self.base, "GET", "/mock/config", chave=None).json
        self.assertNotIn("api_key", cfg)
        self.assertNotIn("webhook_secret", cfg)
        self.assertNotIn(CHAVE, json.dumps(cfg))
        self.assertNotIn(SEGREDO, json.dumps(cfg))
        self.assertEqual(cfg["gateway_webhook_url"], self.gateway.url)
        self.assertIn("sem_reentrega", cfg)
        self.assertNotIn("number_como_string", cfg)

    def test_corpo_invalido_e_cliente_sem_telefone(self) -> None:
        req = urllib.request.Request(self.api + "/tickets", data=b"{nao e json", method="POST")  # noqa: S310
        req.add_header("Authorization", f"Bearer {CHAVE}")
        req.add_header("Content-Type", "application/json")
        with self.assertRaises(urllib.error.HTTPError) as ctx:
            urllib.request.urlopen(req, timeout=5)  # noqa: S310
        with ctx.exception:
            self.assertEqual(ctx.exception.code, 400)
            self.assertEqual(json.loads(ctx.exception.read())["code"], "VALIDATION_ERROR")
        # Content-Length inválido não derruba a conexão: 400 no formato de erro.
        req = urllib.request.Request(self.api + "/tickets", data=b"{}", method="POST")  # noqa: S310
        req.add_header("Authorization", f"Bearer {CHAVE}")
        req.add_header("Content-Length", "abc")
        with self.assertRaises(urllib.error.HTTPError) as ctx:
            urllib.request.urlopen(req, timeout=5)  # noqa: S310
        with ctx.exception:
            self.assertEqual(ctx.exception.code, 400)
        self.assert_erro(
            chamar(self.base, "POST", "/mock/cliente/mensagem", {"texto": "oi"}, chave=None),
            400,
            "VALIDATION_ERROR",
        )
        self.assert_erro(
            chamar(
                self.base,
                "POST",
                "/mock/cliente/mensagem",
                {"telefone": "+55", "texto": ""},
                chave=None,
            ),
            400,
            "VALIDATION_ERROR",
        )

    def test_429_a_cada_n_requisicoes_com_code_e_retry_after(self) -> None:
        self.mock.config["a_cada_429"] = 3
        self.mock.contador_requisicoes = 0
        status = [chamar(self.api, "GET", "/desks").status for _ in range(6)]
        self.assertEqual(status, [200, 200, 429, 200, 200, 429])
        r = chamar(self.api, "GET", "/desks")  # 7ª → ok; 9ª seria 429
        self.assertEqual(r.status, 200)
        r = chamar(self.api, "GET", "/desks")
        r = chamar(self.api, "GET", "/desks")
        erro = self.assert_erro(r, 429, "RATE_LIMITED")
        self.assertEqual(r.headers.get("Retry-After"), "1")
        self.assertIn("Limite", erro["message"])
        # sem chave nunca conta nem devolve 429
        self.assert_erro(chamar(self.api, "GET", "/desks", chave=None), 401, "UNAUTHORIZED")

    def test_todo_erro_da_api_tem_code_documentado(self) -> None:
        casos = [
            chamar(self.api, "GET", "/desks", chave=None),
            chamar(self.api, "GET", "/nada"),
            chamar(self.api, "GET", "/tickets/1"),
            chamar(self.api, "POST", "/tickets", {}),
            chamar(self.api, "POST", "/tickets", {"deskId": "x", "title": "x"}),
            chamar(self.api, "GET", "/conversations/x"),
            chamar(self.api, "POST", "/conversations/x/messages", {"body": "x"}),
            chamar(self.api, "GET", "/events?after=x"),
        ]
        for r in casos:
            self.assertGreaterEqual(r.status, 400)
            self.assertIn(r.json["code"], CODES_DOCUMENTADOS, r.corpo)
            self.assertEqual(set(r.json) - {"errors"}, {"statusCode", "code", "message"})


if __name__ == "__main__":
    unittest.main(verbosity=2)
