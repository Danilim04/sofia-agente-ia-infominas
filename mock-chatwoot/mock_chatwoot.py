#!/usr/bin/env python3
"""Mock do Chatwoot para testar o fluxo ponta a ponta SEM WhatsApp/Chatwoot real.

O mock finge ser o Chatwoot nos dois sentidos do transporte — sem tocar no Go:

  Você (front)  ──"message_created"──►  backend Go  /webhook?token=...
                                            │ (gate de identidade → cérebro Python)
       front  ◄── POST .../messages ────────┘  (Go "responde no Chatwoot" = aqui)

Ou seja:
  1. Quando você digita uma mensagem, o mock monta um JSON `message_created`
     IGUAL ao que o Chatwoot envia e faz POST no webhook do Go.
  2. O Go processa (gate → Contrato A → grafo) e, para responder, chama a REST
     API do Chatwoot. Basta apontar `CHATWOOT_BASE_URL` do Go para este mock:
     ele implementa `.../conversations/{id}/messages`, `.../labels` e
     `.../toggle_status`, captura a resposta do bot e mostra no chat.
  3. O botão "Resolver conversa" faz o papel do atendente: status → resolved,
     etiquetas de fila removidas e `conversation_updated` emitido para o Go
     (que apaga o estado do gate daquela conversa).

Rodar:
    python3 mock_chatwoot.py                  # porta 9000 (default)
    MOCK_PORT=9000 WEBHOOK_TOKEN=dev-token python3 mock_chatwoot.py

Pré-requisitos no ar: backend Go (porta 8080) e cérebro Python (porta 8001).
Veja o README desta pasta para o `.env` do Go que faz o Go falar com o mock.

Stdlib apenas — nenhum `pip install`.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import threading
import time
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse


# ---------------------------------------------------------------------------
# Configuração (env com fallback; sobrescrevível por flags da CLI)
# ---------------------------------------------------------------------------

CONFIG = {
    "port": int(os.environ.get("MOCK_PORT", "9000")),
    "go_webhook": os.environ.get("GO_WEBHOOK_URL", "http://localhost:8080/webhook"),
    "token": os.environ.get("WEBHOOK_TOKEN", ""),
    "account_id": os.environ.get("CHATWOOT_ACCOUNT_ID", "1"),
    "label_bot": os.environ.get("LABEL_BOT", "fila-bot"),
}


# ---------------------------------------------------------------------------
# Estado em memória — transcrições por conversa (conversation_id → mensagens)
# ---------------------------------------------------------------------------

_LOCK = threading.Lock()
_SEQ = 0
_CONVERSATIONS: dict[int, dict] = {}

# Rota que o Go usa para "responder no Chatwoot":
#   POST {baseURL}/api/v1/accounts/{accountID}/conversations/{convID}/{messages|labels|toggle_status}
_CW_ROUTE = re.compile(
    r"^/api/v1/accounts/\d+/conversations/(\d+)/(messages|labels|toggle_status)$"
)


def _add_message(conv_id: int, role: str, content: str, phone: str | None = None) -> dict:
    """Acrescenta uma mensagem à transcrição da conversa e devolve o registro."""
    global _SEQ
    with _LOCK:
        conv = _CONVERSATIONS.setdefault(
            conv_id, {"phone": phone, "labels": [CONFIG["label_bot"]], "status": "open", "messages": []}
        )
        if phone:
            conv["phone"] = phone
        _SEQ += 1
        msg = {"id": _SEQ, "role": role, "content": content, "ts": time.time()}
        conv["messages"].append(msg)
        return msg


def _snapshot(conv_id: int, since: int) -> dict:
    """Mensagens com id > since (para polling do front) + etiquetas atuais."""
    with _LOCK:
        conv = _CONVERSATIONS.get(conv_id)
        if not conv:
            return {"messages": [], "labels": [CONFIG["label_bot"]], "status": "open"}
        novas = [m for m in conv["messages"] if m["id"] > since]
        return {"messages": novas, "labels": list(conv["labels"]), "status": conv.get("status", "open")}


# ---------------------------------------------------------------------------
# Saída → Go: emite o webhook `message_created` (mesmo shape do Chatwoot)
# ---------------------------------------------------------------------------


def _post_para_go(conv_id: int, payload: dict) -> None:
    """Faz POST de um evento (mesmo shape do Chatwoot) no /webhook do Go.

    O Go responde 200 rápido (só enfileira); a resposta do bot chega depois,
    de forma assíncrona, via os endpoints REST mockados abaixo.
    """
    url = CONFIG["go_webhook"]
    if CONFIG["token"]:
        sep = "&" if "?" in url else "?"
        url = f"{url}{sep}token={CONFIG['token']}"

    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(url, data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    # Delivery id único → passa pela deduplicação do Go (cada evento é processado).
    req.add_header("X-Chatwoot-Delivery", uuid.uuid4().hex)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            if resp.status >= 400:
                _add_message(conv_id, "system", f"⚠️ Go respondeu {resp.status} ao webhook")
    except urllib.error.URLError as exc:
        _add_message(
            conv_id,
            "system",
            f"⚠️ não consegui falar com o backend Go em {CONFIG['go_webhook']}: {exc}. "
            "O Go está no ar (go run ./cmd/bot)?",
        )


def _conversa_payload(conv_id: int, phone: str | None, name: str | None) -> dict:
    """Bloco `conversation` como o Chatwoot serializa — com as etiquetas e o
    status ATUAIS da conversa mockada (é isso que o gate de borda do Go lê)."""
    with _LOCK:
        conv = _CONVERSATIONS.get(conv_id) or {}
        labels = list(conv.get("labels", [CONFIG["label_bot"]]))
        status = conv.get("status", "open")
        phone = phone or conv.get("phone") or ""
    return {
        "id": conv_id,
        "account_id": int(CONFIG["account_id"]),
        "status": status,
        "labels": labels,
        "meta": {"sender": {"name": name or "Cliente Mock", "phone_number": phone}},
    }


def _enviar_webhook_para_go(conv_id: int, phone: str, name: str, content: str, msg_id: int) -> None:
    """Emite `message_created` (contato → bot). Como no Chatwoot, a mensagem do
    contato REABRE a conversa resolvida (sem conversation_created) e as
    etiquetas ficam como estavam — é o cenário do "chamado novo depois de
    resolvido"."""
    with _LOCK:
        conv = _CONVERSATIONS.get(conv_id)
        if conv and conv.get("status") == "resolved":
            conv["status"] = "open"
    payload = {
        "event": "message_created",
        "id": msg_id,
        "content": content,
        "message_type": "incoming",  # contato → bot
        "private": False,
        "sender": {
            "id": 1,
            "name": name or "Cliente Mock",
            "type": "contact",
            "phone_number": phone,
        },
        "conversation": _conversa_payload(conv_id, phone, name),
        "account": {"id": int(CONFIG["account_id"]), "name": "Mock"},
    }
    _post_para_go(conv_id, payload)


def _mudar_status(conv_id: int, novo: str, limpar_etiquetas: bool = False) -> None:
    """Muda o status da conversa e emite `conversation_updated` com a mudança em
    `changed_attributes` — é o que o Go usa para apagar o estado do gate quando
    a conversa é RESOLVIDA. `limpar_etiquetas` imita a automação do Chatwoot do
    cliente, que tira a etiqueta de fila ao resolver."""
    with _LOCK:
        conv = _CONVERSATIONS.setdefault(
            conv_id, {"phone": None, "labels": [CONFIG["label_bot"]], "status": "open", "messages": []}
        )
        anterior = conv.get("status", "open")
        conv["status"] = novo
        if limpar_etiquetas:
            conv["labels"] = []
    if anterior == novo:
        return
    payload = {
        "event": "conversation_updated",
        "changed_attributes": [{"status": {"current_value": novo, "previous_value": anterior}}],
        **_conversa_payload(conv_id, None, None),
    }
    aviso = f"📌 status: {anterior} → {novo}"
    if limpar_etiquetas:
        aviso += "  (etiquetas de fila removidas — como a automação do Chatwoot)"
    _add_message(conv_id, "system", aviso)
    _post_para_go(conv_id, payload)


# ---------------------------------------------------------------------------
# HTTP handler
# ---------------------------------------------------------------------------


class Handler(BaseHTTPRequestHandler):
    server_version = "MockChatwoot/1.0"

    # --- helpers de resposta ------------------------------------------------

    def _json(self, obj: dict, status: int = 200) -> None:
        body = json.dumps(obj).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _html(self, html: str) -> None:
        body = html.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read_json(self) -> dict:
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0:
            return {}
        try:
            return json.loads(self.rfile.read(length).decode("utf-8") or "{}")
        except json.JSONDecodeError:
            return {}

    def log_message(self, fmt: str, *args) -> None:  # noqa: A003 — silencia ruído
        return

    # --- GET ----------------------------------------------------------------

    def do_GET(self) -> None:  # noqa: N802
        parsed = urlparse(self.path)
        if parsed.path == "/":
            self._html(INDEX_HTML)
            return
        if parsed.path == "/api/state":
            q = parse_qs(parsed.query)
            try:
                conv_id = int((q.get("conversation_id") or ["0"])[0])
                since = int((q.get("since") or ["0"])[0])
            except ValueError:
                self._json({"messages": [], "labels": []})
                return
            self._json(_snapshot(conv_id, since))
            return
        self._json({"erro": "not found"}, status=404)

    # --- POST ---------------------------------------------------------------

    def do_POST(self) -> None:  # noqa: N802
        parsed = urlparse(self.path)
        path = parsed.path

        # 1) Front → mock: usuário enviou uma mensagem.
        if path == "/api/send":
            body = self._read_json()
            try:
                conv_id = int(body.get("conversation_id"))
            except (TypeError, ValueError):
                self._json({"erro": "conversation_id inválido"}, status=400)
                return
            phone = str(body.get("phone") or "").strip()
            name = str(body.get("name") or "Cliente Mock").strip()
            content = str(body.get("content") or "").strip()
            if not content:
                self._json({"erro": "content vazio"}, status=400)
                return
            msg = _add_message(conv_id, "user", content, phone=phone)
            _enviar_webhook_para_go(conv_id, phone, name, content, msg["id"])
            self._json({"ok": True, "message_id": msg["id"]})
            return

        # 1b) Front → mock: o "atendente" resolveu a conversa (como no Chatwoot).
        if path == "/api/resolve":
            body = self._read_json()
            try:
                conv_id = int(body.get("conversation_id"))
            except (TypeError, ValueError):
                self._json({"erro": "conversation_id inválido"}, status=400)
                return
            _mudar_status(conv_id, "resolved", limpar_etiquetas=True)
            self._json({"ok": True})
            return

        # 2) Go → mock: o bot "responde no Chatwoot" (REST API mockada).
        m = _CW_ROUTE.match(path)
        if m:
            conv_id = int(m.group(1))
            kind = m.group(2)
            body = self._read_json()
            if kind == "messages":
                # Só ecoamos mensagens de saída do agente (ignora notas privadas).
                content = str(body.get("content") or "")
                if content and not body.get("private"):
                    _add_message(conv_id, "bot", content)
                self._json({"id": int(time.time() * 1000) % 1_000_000, "content": content})
                return
            if kind == "labels":
                labels = body.get("labels") or []
                with _LOCK:
                    conv = _CONVERSATIONS.setdefault(
                        conv_id, {"phone": None, "labels": [], "status": "open", "messages": []}
                    )
                    conv["labels"] = list(labels)
                rotulo = ", ".join(labels) or "—"
                aviso = f"🏷️ etiquetas → [{rotulo}]"
                if CONFIG["label_bot"] not in labels:
                    aviso += "  (saiu da fila do bot → atendimento humano)"
                _add_message(conv_id, "system", aviso)
                self._json({"ok": True})
                return
            if kind == "toggle_status":
                # Como o Chatwoot: mudar o status dispara conversation_updated.
                novo = str(body.get("status") or "").strip().lower()
                if novo:
                    _mudar_status(conv_id, novo)
                self._json({"ok": True})
                return
            self._json({"ok": True})
            return

        self._json({"erro": "not found"}, status=404)


# ---------------------------------------------------------------------------
# Front (HTML + JS vanilla, sem build) — embutido para rodar com 1 arquivo
# ---------------------------------------------------------------------------

INDEX_HTML = r"""<!doctype html>
<html lang="pt-br">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Mock Chatwoot — Azapfy</title>
<style>
  :root { --bg:#0b141a; --panel:#111b21; --bot:#202c33; --user:#005c4b; --sys:#1f2a30; --txt:#e9edef; --mut:#8696a0; }
  * { box-sizing:border-box; }
  body { margin:0; font-family:system-ui,Segoe UI,Roboto,sans-serif; background:var(--bg); color:var(--txt); height:100vh; display:flex; flex-direction:column; }
  header { background:var(--panel); padding:10px 16px; display:flex; gap:10px; align-items:center; flex-wrap:wrap; border-bottom:1px solid #0008; }
  header h1 { font-size:15px; margin:0 12px 0 0; font-weight:600; }
  header input { background:#0e1a21; border:1px solid #2a3942; color:var(--txt); padding:7px 10px; border-radius:8px; font-size:13px; }
  header button { background:var(--user); color:#fff; border:0; padding:7px 12px; border-radius:8px; cursor:pointer; font-size:13px; }
  header button.sec { background:#2a3942; }
  header .pill { font-size:12px; color:var(--mut); }
  #log { flex:1; overflow-y:auto; padding:18px; display:flex; flex-direction:column; gap:8px; }
  .row { display:flex; }
  .row.user { justify-content:flex-end; }
  .row.bot { justify-content:flex-start; }
  .row.system { justify-content:center; }
  .bubble { max-width:72%; padding:8px 12px; border-radius:10px; font-size:14px; line-height:1.4; white-space:pre-wrap; word-break:break-word; }
  .user .bubble { background:var(--user); }
  .bot .bubble { background:var(--bot); }
  .system .bubble { background:transparent; color:var(--mut); font-size:12px; font-style:italic; max-width:90%; text-align:center; }
  footer { background:var(--panel); padding:12px 16px; display:flex; gap:10px; border-top:1px solid #0008; }
  footer input { flex:1; background:#2a3942; border:0; color:var(--txt); padding:11px 14px; border-radius:10px; font-size:14px; }
  footer button { background:var(--user); color:#fff; border:0; padding:0 20px; border-radius:10px; cursor:pointer; font-size:14px; }
  .hint { color:var(--mut); font-size:12px; padding:8px 18px 0; }
</style>
</head>
<body>
  <header>
    <h1>📱 Mock Chatwoot</h1>
    <input id="phone" placeholder="telefone (ex.: +5511999990001)" size="22">
    <button id="novo" class="sec">Nova conversa</button>
    <button id="resolver" class="sec" disabled title="faz o papel do atendente: resolve a conversa no Chatwoot">Resolver conversa</button>
    <span class="pill" id="info">—</span>
  </header>
  <div class="hint" id="hint">Defina um telefone e clique em <b>Nova conversa</b> para começar. A primeira mensagem aciona o gate de identidade (login → confirmação). <b>Resolver conversa</b> imita o atendente encerrando o atendimento; a mensagem seguinte reabre a conversa.</div>
  <div id="log"></div>
  <footer>
    <input id="msg" placeholder="Digite uma mensagem..." autocomplete="off" disabled>
    <button id="send" disabled>Enviar</button>
  </footer>

<script>
let convId = null;
let phone = "";
let lastId = 0;
let polling = null;

const $ = (id) => document.getElementById(id);
const log = $("log");

function addBubble(role, text) {
  const row = document.createElement("div");
  row.className = "row " + role;
  const b = document.createElement("div");
  b.className = "bubble";
  b.textContent = text;
  row.appendChild(b);
  log.appendChild(row);
  log.scrollTop = log.scrollHeight;
}

async function poll() {
  if (!convId) return;
  try {
    const r = await fetch(`/api/state?conversation_id=${convId}&since=${lastId}`);
    const data = await r.json();
    for (const m of data.messages) {
      addBubble(m.role, m.content);
      lastId = Math.max(lastId, m.id);
    }
  } catch (e) { /* backend do mock fora? ignora */ }
}

function novaConversa() {
  phone = $("phone").value.trim();
  if (!phone) { $("phone").focus(); return; }
  convId = Math.floor(Math.random() * 1e9);
  lastId = 0;
  log.innerHTML = "";
  $("info").textContent = `conversa #${convId} · ${phone}`;
  $("hint").style.display = "none";
  $("msg").disabled = false;
  $("send").disabled = false;
  $("resolver").disabled = false;
  $("msg").focus();
  if (polling) clearInterval(polling);
  polling = setInterval(poll, 800);
}

async function enviar() {
  const input = $("msg");
  const content = input.value.trim();
  if (!content || !convId) return;
  input.value = "";
  try {
    await fetch("/api/send", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ conversation_id: convId, phone, content }),
    });
    poll();
  } catch (e) {
    addBubble("system", "⚠️ falha ao enviar para o mock");
  }
}

async function resolver() {
  if (!convId) return;
  try {
    await fetch("/api/resolve", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ conversation_id: convId }),
    });
    poll();
  } catch (e) {
    addBubble("system", "⚠️ falha ao resolver no mock");
  }
}

$("novo").addEventListener("click", novaConversa);
$("resolver").addEventListener("click", resolver);
$("send").addEventListener("click", enviar);
$("msg").addEventListener("keydown", (e) => { if (e.key === "Enter") enviar(); });
$("phone").addEventListener("keydown", (e) => { if (e.key === "Enter") novaConversa(); });
</script>
</body>
</html>
"""


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def main() -> None:
    parser = argparse.ArgumentParser(description="Mock do Chatwoot para teste E2E.")
    parser.add_argument("--port", type=int, default=CONFIG["port"])
    parser.add_argument("--go-webhook", default=CONFIG["go_webhook"], dest="go_webhook")
    parser.add_argument("--token", default=CONFIG["token"])
    parser.add_argument("--account-id", default=CONFIG["account_id"], dest="account_id")
    parser.add_argument("--label-bot", default=CONFIG["label_bot"], dest="label_bot")
    args = parser.parse_args()

    CONFIG.update(
        port=args.port,
        go_webhook=args.go_webhook,
        token=args.token,
        account_id=args.account_id,
        label_bot=args.label_bot,
    )

    server = ThreadingHTTPServer(("0.0.0.0", CONFIG["port"]), Handler)
    print("┌─ Mock Chatwoot ─────────────────────────────────────────────")
    print(f"│ Front:        http://localhost:{CONFIG['port']}")
    print(f"│ Webhook→Go:   {CONFIG['go_webhook']}" + ("  (?token=***)" if CONFIG["token"] else ""))
    print(f"│ Label do bot: {CONFIG['label_bot']}  ·  account_id: {CONFIG['account_id']}")
    print("│ Aponte CHATWOOT_BASE_URL do Go para este host:porta.")
    print("└─────────────────────────────────────────────────────────────")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nencerrando mock...")
        server.shutdown()


if __name__ == "__main__":
    main()
