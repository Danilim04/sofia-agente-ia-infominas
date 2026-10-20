#!/usr/bin/env python3
"""Testes do mock-infodesk (stdlib `unittest`, sem rede externa).

Sobe o mock numa porta livre em thread e um "gateway" falso que recebe os
webhooks, e exercita: 401 sem chave, 403 por escopo, criar/consultar ticket
(number como número e como string), listar conversas, enviar mensagem,
department null, finish, assinatura HMAC do webhook, 429 configurável.

Rodar:  python -I mock-infodesk/mock_infodesk_test.py
"""

from __future__ import annotations

import hashlib
import hmac
import json
import socket
import sys
import threading
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
DESK_SUPORTE = "11111111-1111-4111-8111-111111111101"
DEP_FINANCEIRO = "22222222-2222-4222-8222-222222222202"
USER_ANA = "33333333-3333-4333-8333-333333333301"


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
        cfg["number_como_string"] = False
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
            a_cada_429=0, number_como_string=False, escopos=list(mock_infodesk.ESCOPOS_TODOS)
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

    def assert_erro(self, r: Resposta, status: int) -> dict[str, Any]:
        self.assertEqual(r.status, status, r.corpo)
        self.assertEqual(r.headers.get("Content-Type"), "application/json; charset=utf-8")
        self.assertEqual(r.json["statusCode"], status)
        self.assertIsInstance(r.json["message"], str)
        self.assertTrue(r.json["message"])
        return r.json

    # --- autenticação / autorização -----------------------------------------

    def test_401_sem_chave(self) -> None:
        self.assert_erro(chamar(self.api, "GET", "/desks", chave=None), 401)

    def test_401_chave_errada(self) -> None:
        self.assert_erro(chamar(self.api, "GET", "/desks", chave="ik_live_errada"), 401)

    def test_chave_aceita_por_bearer_e_por_x_api_key(self) -> None:
        self.assertEqual(chamar(self.api, "GET", "/desks").status, 200)
        self.assertEqual(chamar(self.api, "GET", "/desks", header_chave="X-Api-Key").status, 200)

    def test_403_por_escopo_com_mensagem_do_pdf(self) -> None:
        self.mock.config["escopos"] = ["tickets:read", "conversations:read", "conversations:write"]
        erro = self.assert_erro(
            chamar(self.api, "POST", "/tickets", {"deskId": DESK_SUPORTE, "title": "x"}), 403
        )
        self.assertEqual(erro["message"], "Escopo insuficiente. Necessário: tickets:write.")
        # Os de referência não exigem escopo (hipótese A3).
        self.mock.config["escopos"] = []
        self.assertEqual(chamar(self.api, "GET", "/departments").status, 200)
        self.assert_erro(chamar(self.api, "GET", "/conversations"), 403)

    def test_404_rota_desconhecida_no_formato_de_erro(self) -> None:
        self.assert_erro(chamar(self.api, "GET", "/priorities"), 404)

    # --- referência ----------------------------------------------------------

    def test_dados_de_referencia(self) -> None:
        desks = chamar(self.api, "GET", "/desks").json
        self.assertEqual({d["name"] for d in desks}, {"Suporte Técnico", "Financeiro", "Triagem"})
        self.assertTrue(all(set(d) == {"id", "name"} for d in desks))
        deps = chamar(self.api, "GET", "/departments").json
        self.assertEqual({d["name"] for d in deps}, {"Suporte", "Financeiro", "Atendimento"})
        users = chamar(self.api, "GET", "/users").json
        self.assertEqual(len(users), 2)
        self.assertTrue(all(set(u) == {"id", "username", "email"} for u in users))

    # --- tickets ---------------------------------------------------------------

    def test_criar_ticket_valida_obrigatorios(self) -> None:
        erro = self.assert_erro(chamar(self.api, "POST", "/tickets", {"title": "sem mesa"}), 400)
        self.assertIn("deskId", erro["message"])
        erro = self.assert_erro(chamar(self.api, "POST", "/tickets", {"deskId": DESK_SUPORTE}), 400)
        self.assertIn("title", erro["message"])
        self.assert_erro(
            chamar(self.api, "POST", "/tickets", {"deskId": "nao-existe", "title": "x"}), 400
        )

    def test_criar_e_consultar_ticket_number_numerico(self) -> None:
        r = chamar(
            self.api,
            "POST",
            "/tickets",
            {
                "deskId": DESK_SUPORTE,
                "title": "Impressora não imprime",
                "description": "Cliente relata erro ao imprimir.",
                "channel": "WHATSAPP",
                "contactPhone": "+5531990000000",
            },
        )
        self.assertEqual(r.status, 201, r.corpo)
        t = r.json
        self.assertIsInstance(t["number"], int)
        self.assertEqual(t["number"], 1001)
        self.assertEqual(t["deskId"], DESK_SUPORTE)
        self.assertEqual(t["channel"], "WHATSAPP")
        self.assertEqual(
            chamar(self.api, "POST", "/tickets", {"deskId": DESK_SUPORTE, "title": "outro"}).json[
                "number"
            ],
            1002,
        )

        g = chamar(self.api, "GET", f"/tickets/{t['number']}")
        self.assertEqual(g.status, 200)
        self.assertEqual(g.json["title"], "Impressora não imprime")
        self.assert_erro(chamar(self.api, "GET", "/tickets/9999"), 404)
        self.assert_erro(chamar(self.api, "GET", "/tickets/abc"), 404)

    def test_number_como_string_quando_configurado(self) -> None:
        self.mock.config["number_como_string"] = True
        t = chamar(self.api, "POST", "/tickets", {"deskId": DESK_SUPORTE, "title": "x"}).json
        self.assertIsInstance(t["number"], str)
        self.assertEqual(t["number"], "1001")
        self.assertEqual(chamar(self.api, "GET", "/tickets/1001").json["number"], "1001")

    def test_reply_do_ticket(self) -> None:
        t = chamar(self.api, "POST", "/tickets", {"deskId": DESK_SUPORTE, "title": "x"}).json
        self.assert_erro(chamar(self.api, "POST", f"/tickets/{t['number']}/reply", {}), 400)
        r = chamar(
            self.api, "POST", f"/tickets/{t['number']}/reply", {"body": "Já estamos verificando."}
        )
        self.assertEqual(r.status, 201)
        self.assertEqual(r.json["body"], "Já estamos verificando.")
        self.assert_erro(chamar(self.api, "POST", "/tickets/777/reply", {"body": "x"}), 404)

    # --- conversas --------------------------------------------------------------

    def test_listar_conversas_com_tab_e_department(self) -> None:
        self.assertEqual(chamar(self.api, "GET", "/conversations").json, [])
        a = self.cliente_escreve("+5531990000001")["conversationId"]
        b = self.cliente_escreve("+5531990000002", nome="João")["conversationId"]
        lista = chamar(self.api, "GET", "/conversations").json
        self.assertEqual({c["id"] for c in lista}, {a, b})
        item = next(c for c in lista if c["id"] == b)
        self.assertEqual(item["status"], "OPEN")
        self.assertEqual(item["channel"], "WHATSAPP")
        self.assertEqual(item["contact"], {"name": "João", "phone": "+5531990000002"})
        self.assertIsNone(item["departmentId"])
        self.assertIsNone(item["assignedUserId"])
        self.assertTrue(item["lastMessageAt"].endswith("Z"))
        self.assertNotIn("messages", item)

        chamar(self.api, "POST", f"/conversations/{a}/department", {"departmentId": DEP_FINANCEIRO})
        chamar(self.api, "POST", f"/conversations/{a}/assign", {"userId": USER_ANA})
        self.assertEqual(
            [
                c["id"]
                for c in chamar(
                    self.api, "GET", f"/conversations?departmentId={DEP_FINANCEIRO}"
                ).json
            ],
            [a],
        )
        self.assertEqual(
            [c["id"] for c in chamar(self.api, "GET", "/conversations?tab=entrada").json], [b]
        )
        self.assertEqual(
            [c["id"] for c in chamar(self.api, "GET", "/conversations?tab=atendimento").json], [a]
        )
        self.assertEqual(chamar(self.api, "GET", "/conversations?tab=encerrados").json, [])
        self.assert_erro(chamar(self.api, "GET", "/conversations?tab=outra"), 400)

    def test_ler_conversa_com_mensagens(self) -> None:
        conv = self.cliente_escreve(texto="Oi")["conversationId"]
        r = chamar(self.api, "GET", f"/conversations/{conv}")
        self.assertEqual(r.status, 200)
        msgs = r.json["messages"]
        self.assertEqual(len(msgs), 1)
        self.assertEqual(set(msgs[0]), {"id", "body", "sender", "createdAt"})
        self.assertEqual(msgs[0]["sender"], "CLIENT")
        self.assertEqual(msgs[0]["body"], "Oi")
        self.assert_erro(chamar(self.api, "GET", "/conversations/nao-existe"), 404)

    def test_enviar_mensagem_registra_como_bot(self) -> None:
        conv = self.cliente_escreve()["conversationId"]
        self.assert_erro(chamar(self.api, "POST", f"/conversations/{conv}/messages", {}), 400)
        r = chamar(
            self.api,
            "POST",
            f"/conversations/{conv}/messages",
            {"body": "Posso ajudar em algo mais?"},
        )
        self.assertEqual(r.status, 201, r.corpo)
        self.assertEqual(r.json["sender"], "BOT")
        msgs = chamar(self.api, "GET", f"/conversations/{conv}").json["messages"]
        self.assertEqual([m["sender"] for m in msgs], ["CLIENT", "BOT"])
        self.assert_erro(
            chamar(self.api, "POST", "/conversations/nao-existe/messages", {"body": "x"}), 404
        )
        # A mensagem do bot aparece na UI como bolha do bot.
        estado = chamar(self.base, "GET", "/mock/estado?telefone=%2B5531990000001", chave=None).json
        self.assertEqual([m["sender"] for m in estado["conversa"]["messages"]], ["CLIENT", "BOT"])

    def test_department_null_remove(self) -> None:
        conv = self.cliente_escreve()["conversationId"]
        r = chamar(
            self.api, "POST", f"/conversations/{conv}/department", {"departmentId": DEP_FINANCEIRO}
        )
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["departmentId"], DEP_FINANCEIRO)
        r = chamar(self.api, "POST", f"/conversations/{conv}/department", {"departmentId": None})
        self.assertEqual(r.status, 200, r.corpo)
        self.assertIsNone(r.json["departmentId"])
        self.assert_erro(chamar(self.api, "POST", f"/conversations/{conv}/department", {}), 400)
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/department", {"departmentId": "x"}),
            400,
        )

    def test_assign(self) -> None:
        conv = self.cliente_escreve()["conversationId"]
        r = chamar(self.api, "POST", f"/conversations/{conv}/assign", {"userId": USER_ANA})
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["assignedUserId"], USER_ANA)
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/assign", {"userId": "x"}), 400
        )

    def test_finish_encerra_emite_webhook_e_bloqueia_mensagens(self) -> None:
        conv = self.cliente_escreve()["conversationId"]
        self.gateway.esperar(2)
        self.gateway.limpar()
        r = chamar(self.api, "POST", f"/conversations/{conv}/finish")
        self.assertEqual(r.status, 200, r.corpo)
        self.assertEqual(r.json["status"], "FINISHED")
        self.assertIsNotNone(r.json["finishedAt"])
        entregas = self.gateway.esperar(1)
        self.assertEqual([e["event"] for e in entregas], ["conversation.finished"])
        self.assertEqual(entregas[0]["json"]["data"]["conversationId"], conv)
        self.assertEqual(entregas[0]["json"]["data"]["contact"]["phone"], "+5531990000001")
        # idempotente: segundo finish não emite de novo
        self.assertEqual(chamar(self.api, "POST", f"/conversations/{conv}/finish").status, 200)
        self.assertEqual(len(self.gateway.esperar(2, timeout=0.3)), 1)
        self.assertEqual(
            [c["id"] for c in chamar(self.api, "GET", "/conversations?tab=encerrados").json], [conv]
        )
        self.assert_erro(
            chamar(self.api, "POST", f"/conversations/{conv}/messages", {"body": "x"}), 404
        )
        # Próxima mensagem do mesmo telefone abre OUTRA conversa (hipótese C5).
        self.assertNotEqual(self.cliente_escreve()["conversationId"], conv)

    # --- webhooks ------------------------------------------------------------

    def test_webhook_assinado_com_payload_do_pdf(self) -> None:
        res = self.cliente_escreve(texto="Bom dia, preciso de ajuda")
        entregas = self.gateway.esperar(2)
        self.assertEqual(
            [e["event"] for e in entregas], ["conversation.started", "message.received"]
        )
        for e in entregas:
            self.assertEqual(e["path"], "/webhook")
            self.assertEqual(e["content_type"], "application/json")
            esperado = "sha256=" + hmac.new(SEGREDO.encode(), e["raw"], hashlib.sha256).hexdigest()
            self.assertEqual(e["signature"], esperado)
            self.assertEqual(e["json"]["event"], e["event"])
            self.assertEqual(e["json"]["organizationId"], self.mock.config["org_id"])
            self.assertRegex(
                e["json"]["createdAt"], r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$"
            )
            self.assertEqual(e["json"]["data"]["conversationId"], res["conversationId"])
            self.assertEqual(
                e["json"]["data"]["contact"], {"name": "Maria", "phone": "+5531990000001"}
            )
        # W1: started/finished não levam `message`.
        self.assertEqual(set(entregas[0]["json"]["data"]), {"conversationId", "channel", "contact"})
        recebida = entregas[1]["json"]
        self.assertEqual(set(recebida), {"id", "event", "createdAt", "organizationId", "data"})
        self.assertEqual(set(recebida["data"]), {"conversationId", "channel", "contact", "message"})
        self.assertEqual(recebida["data"]["channel"], "WHATSAPP")
        msg = recebida["data"]["message"]
        self.assertEqual(set(msg), {"id", "body", "sender", "createdAt"})
        self.assertEqual(msg["sender"], "CLIENT")
        self.assertEqual(msg["body"], "Bom dia, preciso de ajuda")
        self.assertEqual(msg["id"], res["messageId"])
        self.assertNotEqual(entregas[0]["json"]["id"], recebida["id"])  # id de entrega único
        self.assertTrue(all(x["ok"] for x in res["entregas"]))
        # Segunda mensagem do mesmo telefone: só message.received, mesma conversa.
        self.gateway.limpar()
        res2 = self.cliente_escreve(texto="Ainda estou esperando")
        self.assertEqual(res2["conversationId"], res["conversationId"])
        self.assertEqual([e["event"] for e in self.gateway.esperar(1)], ["message.received"])

    def test_audio_e_message_received_com_body_vazio(self) -> None:
        r = chamar(
            self.base,
            "POST",
            "/mock/cliente/audio",
            {"telefone": "+5531990000009", "nome": "Zé"},
            chave=None,
        )
        self.assertEqual(r.status, 201, r.corpo)
        entregas = self.gateway.esperar(2)
        self.assertEqual(entregas[1]["json"]["data"]["message"]["body"], "")
        self.assertEqual(entregas[1]["json"]["data"]["message"]["sender"], "CLIENT")

    def test_falha_de_entrega_fica_registrada_sem_reentrega(self) -> None:
        self.gateway.status = 500
        res = self.cliente_escreve()
        self.assertEqual(len(self.gateway.esperar(2)), 2)  # tentou uma vez cada
        self.assertTrue(all(not x["ok"] and x["status"] == 500 for x in res["entregas"]))
        self.gateway.status = 200
        self.assertEqual(len(self.gateway.esperar(3, timeout=0.3)), 2)  # nenhuma reentrega
        estado = chamar(self.base, "GET", "/mock/estado?telefone=%2B5531990000001", chave=None).json
        self.assertTrue(any("falhou" in e["texto"] for e in estado["conversa"]["eventos"]))

    def test_gateway_fora_do_ar_nao_derruba_o_mock(self) -> None:
        self.mock.config["gateway_webhook_url"] = "http://127.0.0.1:1/webhook"
        try:
            res = self.cliente_escreve()
            self.assertFalse(res["entregas"][0]["ok"])
            self.assertIn("sem resposta do gateway", res["entregas"][0]["erro"])
        finally:
            self.mock.config["gateway_webhook_url"] = self.gateway.url

    # --- 429 -------------------------------------------------------------------

    def test_429_a_cada_n_requisicoes_com_retry_after(self) -> None:
        self.mock.config["a_cada_429"] = 3
        self.mock.contador_requisicoes = 0
        status = [chamar(self.api, "GET", "/desks").status for _ in range(6)]
        self.assertEqual(status, [200, 200, 429, 200, 200, 429])
        r = chamar(self.api, "GET", "/desks")  # 7ª → ok; 9ª seria 429
        self.assertEqual(r.status, 200)
        r = chamar(self.api, "GET", "/desks")
        r = chamar(self.api, "GET", "/desks")
        erro = self.assert_erro(r, 429)
        self.assertEqual(r.headers.get("Retry-After"), "1")
        self.assertIn("Limite", erro["message"])
        # sem chave nunca conta nem devolve 429
        self.assert_erro(chamar(self.api, "GET", "/desks", chave=None), 401)

    # --- UI / controle -----------------------------------------------------------

    def test_ui_e_estado(self) -> None:
        r = chamar(self.base, "GET", "/", chave=None)
        self.assertEqual(r.status, 200)
        self.assertIn("text/html", r.headers.get("Content-Type"))
        self.assertIn("Mock Infodesk", r.corpo.decode("utf-8"))
        self.cliente_escreve()
        conv = chamar(self.api, "GET", "/conversations").json[0]["id"]
        chamar(
            self.api, "POST", f"/conversations/{conv}/department", {"departmentId": DEP_FINANCEIRO}
        )
        chamar(
            self.api,
            "POST",
            "/tickets",
            {"deskId": DESK_SUPORTE, "title": "Sem internet", "contactPhone": "+5531990000001"},
        )
        estado = chamar(self.base, "GET", "/mock/estado?telefone=%2B5531990000001", chave=None).json
        self.assertEqual(estado["conversa"]["id"], conv)
        self.assertEqual(estado["conversa"]["departmentName"], "Financeiro")
        self.assertEqual(estado["tickets"][0]["title"], "Sem internet")
        self.assertEqual(estado["tickets"][0]["deskName"], "Suporte Técnico")
        self.assertEqual(
            [e["event"] for e in estado["entregas"]], ["conversation.started", "message.received"]
        )
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
        )

    def test_log_nao_expoe_telefone_nem_query_string(self) -> None:
        linhas: list[str] = []
        self.mock.log = linhas.append
        try:
            self.cliente_escreve("+5531998877665", texto="oi")
            chamar(self.base, "GET", "/mock/estado?telefone=%2B5531998877665", chave=None)
            chamar(self.api, "GET", "/conversations?departmentId=5531998877665")
        finally:
            self.mock.log = lambda *_a, **_k: None
        texto = "\n".join(linhas)
        self.assertIn("[mock] GET /mock/estado → 200", texto)
        self.assertNotIn("telefone=", texto)
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
        self.assertEqual(json.loads(texto.split("\r\n\r\n", 1)[1])["statusCode"], 400)

    def test_405_metodo_errado_em_rota_conhecida(self) -> None:
        self.assert_erro(chamar(self.api, "GET", "/tickets"), 405)
        self.assert_erro(chamar(self.api, "POST", "/desks", {}), 405)

    def test_referencia_exige_escopo_quando_configurado(self) -> None:
        self.mock.config["referencia_exige_escopo"] = True
        try:
            self.mock.config["escopos"] = ["tickets:read"]
            self.assertEqual(chamar(self.api, "GET", "/desks").status, 200)
            self.assert_erro(chamar(self.api, "GET", "/users"), 403)
            self.assert_erro(chamar(self.api, "GET", "/departments"), 403)
        finally:
            self.mock.config["referencia_exige_escopo"] = False

    def test_finish_sem_webhook_quando_configurado(self) -> None:
        conv = self.cliente_escreve()["conversationId"]
        self.gateway.esperar(2)
        self.gateway.limpar()
        self.mock.config["finish_emite_webhook"] = False
        try:
            self.assertEqual(
                chamar(self.api, "POST", f"/conversations/{conv}/finish").json["status"], "FINISHED"
            )
            self.assertEqual(self.gateway.esperar(1, timeout=0.3), [])
        finally:
            self.mock.config["finish_emite_webhook"] = True

    def test_mock_config_nao_expoe_segredos(self) -> None:
        cfg = chamar(self.base, "GET", "/mock/config", chave=None).json
        self.assertNotIn("api_key", cfg)
        self.assertNotIn("webhook_secret", cfg)
        self.assertNotIn(CHAVE, json.dumps(cfg))
        self.assertNotIn(SEGREDO, json.dumps(cfg))
        self.assertEqual(cfg["gateway_webhook_url"], self.gateway.url)

    def test_corpo_invalido_e_cliente_sem_telefone(self) -> None:
        req = urllib.request.Request(self.api + "/tickets", data=b"{nao e json", method="POST")  # noqa: S310
        req.add_header("Authorization", f"Bearer {CHAVE}")
        req.add_header("Content-Type", "application/json")
        with self.assertRaises(urllib.error.HTTPError) as ctx:
            urllib.request.urlopen(req, timeout=5)  # noqa: S310
        with ctx.exception:
            self.assertEqual(ctx.exception.code, 400)
        # Content-Length inválido não derruba a conexão: 400 no formato de erro.
        req = urllib.request.Request(self.api + "/tickets", data=b"{}", method="POST")  # noqa: S310
        req.add_header("Authorization", f"Bearer {CHAVE}")
        req.add_header("Content-Length", "abc")
        with self.assertRaises(urllib.error.HTTPError) as ctx:
            urllib.request.urlopen(req, timeout=5)  # noqa: S310
        with ctx.exception:
            self.assertEqual(ctx.exception.code, 400)
        self.assert_erro(
            chamar(self.base, "POST", "/mock/cliente/mensagem", {"texto": "oi"}, chave=None), 400
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
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
