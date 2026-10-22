#!/usr/bin/env python3
"""Coleta as conversas de WhatsApp do Infodesk para virarem material de eval
da Sofia (cenários de conversa e dataset de classificação).

Só faz `GET`. Stdlib pura (sem pip), como `analisar-infodesk.py`.

    set -a && . ./.env && set +a      # INFODESK_BASE_URL e INFODESK_API_KEY
    python scripts/coletar-conversas-infodesk.py --meses 12

A chave vem SEMPRE do ambiente. Pagina `GET /messages?since=&channel=WHATSAPP`,
descarta nota interna (`internal: true`), agrupa por conversa (ordem
cronológica), descarta grupos de WhatsApp (não passam pelo bot) e junta
`status`/`departmentId`/`ticketNumber` de `GET /conversations`.

PRIVACIDADE (CLAUDE.md regra 5; D12 em docs/plano/00): o bruto tem nome e
telefone de cliente e **nunca** vai para o git nem para o terminal. Saem três
arquivos, todos em `agente-ia/evals/bruto/` (gitignored; o script se recusa a
gravar em outro lugar que o git não ignore):

- `conversas-whatsapp-<data>.json`      bruto, com contato e `senderName`;
- `conversas-whatsapp-<data>-pre.json`  igual, mas `senderName`/contato viram
  tokens e o id da conversa vira hash — é este que entra no
  `python -m evals.anonimizar`;
- `conversas-whatsapp-<data>-nomes.txt` nomes de contato e de autor (para o
  `--nomes` do anonimizador: o regex não acha nome próprio sozinho).

O terminal recebe só contagens (conversas, mensagens, meses, descartes).
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request
from collections import Counter
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

PREFIXO = "/integration/v1"
PAUSA_S = 0.25  # rate limit do Desk: 300 req/min por IP (07, A4)
TIMEOUT_S = 30
TENTATIVAS = 6
ESPERA_429_S = 5.0  # dobra a cada tentativa; `Retry-After`, se vier, vence
CANAL = "WHATSAPP"
LIMITE_MINUSCULA = 20  # ≥ N usos em minúscula (e ≥ 80% dos usos) = palavra comum, não nome
LIMITE_LACO = 500  # conversa com ≥ N mensagens e < 5% de corpos distintos = laço de eco
SEM = "(sem valor)"
RAIZ = Path(__file__).resolve().parent.parent
DESTINO_PADRAO = RAIZ / "agente-ia" / "evals" / "bruto"

Json = dict[str, Any]


# --------------------------------------------------------------------------- HTTP
class ErroAPI(Exception):
    """Guarda só status e `code`; o corpo da resposta nunca vai para a mensagem."""

    def __init__(self, status: int, code: str, rota: str) -> None:
        super().__init__(f"{rota}: HTTP {status} {code}".strip())
        self.status = status


class Cliente:
    def __init__(self, base_url: str, api_key: str) -> None:
        base = base_url.strip().rstrip("/")
        if not base.endswith(PREFIXO):
            base += PREFIXO
        self.base = base
        self.api_key = api_key
        self.chamadas = 0

    def get(self, rota: str, **query: str) -> Any:
        url = self.base + rota
        if query:
            url += "?" + urllib.parse.urlencode(query)
        espera = ESPERA_429_S
        for tentativa in range(1, TENTATIVAS + 1):
            req = urllib.request.Request(  # noqa: S310 — só http(s), URL vem do operador
                url,
                headers={"Authorization": f"Bearer {self.api_key}", "Accept": "application/json"},
                method="GET",
            )
            try:
                with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:  # noqa: S310
                    dados = json.loads(resp.read().decode("utf-8") or "null")
                self.chamadas += 1
                time.sleep(PAUSA_S)
                return dados
            except urllib.error.HTTPError as exc:
                corpo = exc.read().decode("utf-8", "replace")
                transitorio = exc.code == 429 or exc.code >= 500
                if transitorio and tentativa < TENTATIVAS:
                    try:
                        espera_hdr = float(exc.headers.get("Retry-After", ""))
                    except ValueError:
                        espera_hdr = 0.0
                    pausa = max(espera, espera_hdr)
                    print(
                        f"  HTTP {exc.code} em {rota}: esperando {pausa:.0f}s "
                        f"(tentativa {tentativa}/{TENTATIVAS})",
                        file=sys.stderr,
                    )
                    time.sleep(pausa)
                    espera *= 2
                    continue
                raise ErroAPI(exc.code, _code(corpo), rota) from None
            except urllib.error.URLError as exc:
                if tentativa < TENTATIVAS:
                    time.sleep(espera)
                    espera *= 2
                    continue
                raise ErroAPI(0, str(exc.reason)[:80], rota) from None
        raise ErroAPI(0, "tentativas esgotadas", rota)


def _code(corpo: str) -> str:
    try:
        d = json.loads(corpo)
    except ValueError:
        return ""
    return str(d.get("code", "")) if isinstance(d, dict) else ""


def itens(dados: Any) -> list[Json]:
    """Array puro ou `{data: [...]}` (07, D3)."""
    if isinstance(dados, dict) and isinstance(dados.get("data"), list):
        return dados["data"]
    return dados if isinstance(dados, list) else []


def proximo_cursor(dados: Any) -> str:
    return str(dados.get("nextCursor") or "") if isinstance(dados, dict) else ""


def paginar(cli: Cliente, rota: str, limite_paginas: int, **query: str) -> list[Json]:
    """Todas as páginas de `rota` (`{data, nextCursor}`), até `limite_paginas`."""
    todos: list[Json] = []
    cursor = ""
    for _ in range(limite_paginas):
        q = dict(query)
        if cursor:
            q["cursor"] = cursor
        pagina = cli.get(rota, **q)
        pagina_itens = itens(pagina)
        todos.extend(pagina_itens)
        cursor = proximo_cursor(pagina)
        # Observado no Infodesk: `/messages` segue devolvendo `nextCursor` com páginas
        # vazias depois do fim; página vazia encerra (senão são centenas de GET à toa).
        if not cursor or not pagina_itens:
            return todos
    print(f"  aviso: {rota} parou em {limite_paginas} páginas (há mais)", file=sys.stderr)
    return todos


# --------------------------------------------------------------------------- agrupamento
def _mensagem(m: Json) -> Json:
    """Único ponto onde uma mensagem crua é lida: só o que o eval precisa."""
    return {
        "sender": str(m.get("sender") or SEM),
        "senderName": str(m.get("senderName") or ""),
        "type": str(m.get("type") or "TEXT"),
        "body": str(m.get("body") or ""),
        "createdAt": str(m.get("createdAt") or ""),
    }


def agrupar(mensagens: list[Json], conversas: list[Json]) -> tuple[list[Json], Counter[str]]:
    """Mensagens → conversas ordenadas. Devolve também os descartes (contagens)."""
    descartes: Counter[str] = Counter()
    meta = {c["id"]: c for c in conversas if c.get("id")}
    por_conversa: dict[str, list[Json]] = {}
    contato: dict[str, Json] = {}
    for m in mensagens:
        if m.get("internal"):
            descartes["mensagem interna"] += 1
            continue
        cid = m.get("conversationId")
        if not cid:
            descartes["mensagem sem conversa"] += 1
            continue
        por_conversa.setdefault(cid, []).append(_mensagem(m))
        if isinstance(m.get("contact"), dict):
            contato.setdefault(cid, m["contact"])
    saida: list[Json] = []
    for cid, msgs in por_conversa.items():
        c = meta.get(cid, {})
        ct = c.get("contact") if isinstance(c.get("contact"), dict) else contato.get(cid, {})
        if ct.get("groupJid"):
            descartes["conversa de grupo"] += 1
            continue
        if len(msgs) >= LIMITE_LACO and len({m["body"] for m in msgs}) / len(msgs) < 0.05:
            descartes["conversa em laço (corpos repetidos)"] += 1  # eco bot ↔ bot, não é cliente
            continue
        if not c:
            descartes["conversa sem metadados (mantida)"] += 1
        msgs.sort(key=lambda x: x["createdAt"])  # estável: preserva a ordem da API no empate
        saida.append(
            {
                "id": cid,
                "status": str(c.get("status") or SEM),
                "departmentId": c.get("departmentId") or None,
                "ticketNumber": c.get("ticketNumber"),
                "sessionStartedAt": c.get("sessionStartedAt") or None,
                "contato": {
                    "nome": str(ct.get("name") or ""),
                    "telefone": str(ct.get("phone") or ""),
                },
                "mensagens": msgs,
            }
        )
    saida.sort(key=lambda c: c["mensagens"][0]["createdAt"])
    return saida, descartes


# --------------------------------------------------------------------------- pré-anonimização
_RE_PALAVRA = re.compile(r"[^\W\d_]{3,}", re.UNICODE)
# Palavras comuns que aparecem em nome de contato ("Sala 2", "Suporte") ou em
# sobrenome composto e que destruiriam o texto se virassem <NOME>.
_COMUNS = frozenset(
    "de da do das dos e com para por sem sala loja empresa ltda eireli epp mei suporte "
    "escritorio escritório clinica clínica casa filial matriz grupo servico serviço servicos "
    "serviços comercial financeiro bot atendimento atendente sistema infominas".split()
)


def _sem_acento(t: str) -> str:
    return "".join(c for c in unicodedata.normalize("NFD", t) if unicodedata.category(c) != "Mn")


def palavras_do_dia_a_dia(conversas: list[Json]) -> set[str]:
    """Palavras que o corpus escreve quase sempre em minúscula (`rede`, `todo`): nome de
    contato como "Rede Fulano" não pode transformar `rede` em `<NOME>` no texto inteiro."""
    minuscula: Counter[str] = Counter()
    total: Counter[str] = Counter()
    for c in conversas:
        for m in c["mensagens"]:
            for p in _RE_PALAVRA.findall(m["body"]):
                chave = _sem_acento(p).lower()
                total[chave] += 1
                if p.islower():
                    minuscula[chave] += 1
    return {k for k, n in minuscula.items() if n >= LIMITE_MINUSCULA and n >= 0.8 * total[k]}


def nomes_de(conversas: list[Json], extras: tuple[str, ...] = ()) -> list[str]:
    """Nomes de contato e de autor (completos e por palavra) para o `--nomes` do anonimizador.

    `extras`: nomes próprios comuns (clientes assinam e citam colegas que não estão no
    cadastro). Palavras do dia a dia ficam de fora, como em `palavras_do_dia_a_dia`."""
    comuns = palavras_do_dia_a_dia(conversas)
    achados: set[str] = set()
    fontes = [c["contato"]["nome"] for c in conversas]
    fontes += [m["senderName"] for c in conversas for m in c["mensagens"]]
    for nome in [*fontes, *extras]:
        nome = " ".join(nome.split())
        if len(nome) < 3:
            continue
        if len(nome.split()) > 1:
            achados.add(nome)
        for p in _RE_PALAVRA.findall(nome):
            chave = _sem_acento(p).lower()
            if chave not in _COMUNS and chave not in comuns:
                achados.add(p)
    return sorted(achados, key=lambda n: (-len(n), n.lower()))


def pseudonimo(valor: str) -> str:
    return hashlib.sha256(valor.encode("utf-8")).hexdigest()[:12]


def preparar(conversas: list[Json]) -> list[Json]:
    """Cópia sem identificadores diretos: `senderName`/contato viram token; id vira hash."""
    saida: list[Json] = []
    for c in conversas:
        msgs = []
        for m in c["mensagens"]:
            papel = {"CLIENT": "<CLIENTE>", "AGENT": "<ATENDENTE>"}.get(m["sender"], "")
            msgs.append({**m, "senderName": papel if m["senderName"] else ""})
        saida.append(
            {
                **c,
                "id": "conv-" + pseudonimo(c["id"]),
                "contato": {"nome": "<NOME>", "telefone": "<TEL>"},
                "mensagens": msgs,
            }
        )
    return saida


# --------------------------------------------------------------------------- saída
def garantir_ignorado(destino: Path) -> None:
    """Recusa gravar o bruto onde o git não ignora (PII no git é o risco do projeto)."""
    teste = destino / "conversas-whatsapp-teste.json"
    r = subprocess.run(  # noqa: S603 — comando fixo
        ["git", "check-ignore", "-q", str(teste)],  # noqa: S607
        cwd=RAIZ,
        capture_output=True,
        check=False,
    )
    if r.returncode != 0:
        sys.exit(f"erro: {destino} não está no .gitignore; recusando gravar dados pessoais.")


def gravar_json(caminho: Path, dado: Any) -> None:
    caminho.parent.mkdir(parents=True, exist_ok=True)
    caminho.write_text(json.dumps(dado, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")


def resumo(conversas: list[Json], descartes: Counter[str], chamadas: int) -> str:
    msgs = sum(len(c["mensagens"]) for c in conversas)
    meses = Counter(c["mensagens"][0]["createdAt"][:7] for c in conversas)
    tipos = Counter(m["type"] for c in conversas for m in c["mensagens"])
    remet = Counter(m["sender"] for c in conversas for m in c["mensagens"])
    com_dep = sum(1 for c in conversas if c["departmentId"])
    com_ticket = sum(1 for c in conversas if c["ticketNumber"] is not None)
    linhas = [
        f"{len(conversas)} conversa(s), {msgs} mensagem(ns), {len(meses)} mês(es), "
        f"{chamadas} chamada(s) GET",
        f"  com departamento: {com_dep} · com ticket: {com_ticket}",
        "  remetente: " + ", ".join(f"{k}={v}" for k, v in sorted(remet.items())),
        "  tipo: " + ", ".join(f"{k}={v}" for k, v in sorted(tipos.items())),
        "  conversas por mês (1ª mensagem): "
        + ", ".join(f"{k}={v}" for k, v in sorted(meses.items())),
    ]
    if descartes:
        linhas.append("  descartes: " + ", ".join(f"{k}={v}" for k, v in sorted(descartes.items())))
    return "\n".join(linhas)


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(
        description="Coleta conversas de WhatsApp do Infodesk (só GET) para evals; saída em evals/bruto/."
    )
    p.add_argument("--meses", type=int, default=12, help="janela em meses (default 12)")
    p.add_argument("--saida", type=Path, default=DESTINO_PADRAO, help="pasta gitignored do bruto")
    p.add_argument("--limite-paginas", type=int, default=1000, help="teto de páginas por rota")
    p.add_argument(
        "--de-arquivo",
        type=Path,
        help="não chama a API: refaz o `-pre.json` e o `-nomes.txt` a partir deste bruto",
    )
    p.add_argument(
        "--nomes-extra",
        type=Path,
        help="arquivo com nomes próprios comuns (um por linha, `#` comenta) somados à lista",
    )
    args = p.parse_args(argv)
    garantir_ignorado(args.saida)
    extras: tuple[str, ...] = ()
    if args.nomes_extra:
        linhas = args.nomes_extra.read_text(encoding="utf-8").splitlines()
        extras = tuple(x.strip() for x in linhas if x.strip() and not x.startswith("#"))
    if args.de_arquivo:
        dado = json.loads(args.de_arquivo.read_text(encoding="utf-8"))
        base_nome = args.de_arquivo.with_suffix("")
        pre = {**dado, "conversas": preparar(dado["conversas"])}
        gravar_json(Path(f"{base_nome}-pre.json"), pre)
        nomes_txt = "\n".join(nomes_de(dado["conversas"], extras)) + "\n"
        Path(f"{base_nome}-nomes.txt").write_text(nomes_txt, encoding="utf-8")
        print(f"refeitos {base_nome.name}-pre.json e {base_nome.name}-nomes.txt")
        return 0

    base = os.environ.get("INFODESK_BASE_URL", "").strip()
    chave = os.environ.get("INFODESK_API_KEY", "").strip()
    if not base or not chave:
        sys.exit(
            "erro: defina INFODESK_BASE_URL e INFODESK_API_KEY no ambiente (set -a && . ./.env)."
        )

    agora = datetime.now(UTC)
    desde = (agora - timedelta(days=round(args.meses * 30.4375))).strftime("%Y-%m-%dT%H:%M:%SZ")
    cli = Cliente(base, chave)
    try:
        print(f"coletando mensagens de {CANAL} desde {desde[:10]} …")
        mensagens = paginar(
            cli, "/messages", args.limite_paginas, since=desde, channel=CANAL, limit="500"
        )
        print(f"  {len(mensagens)} mensagem(ns) recebida(s)")
        conversas_api = paginar(
            cli, "/conversations", args.limite_paginas, tab="todas", channel=CANAL, limit="100"
        )
        print(f"  {len(conversas_api)} conversa(s) nos metadados")
    except ErroAPI as exc:
        sys.exit(f"erro: {exc}")

    conversas, descartes = agrupar(mensagens, conversas_api)
    data = agora.strftime("%Y-%m-%d")
    bruto = args.saida / f"conversas-whatsapp-{data}.json"
    pre = args.saida / f"conversas-whatsapp-{data}-pre.json"
    nomes = args.saida / f"conversas-whatsapp-{data}-nomes.txt"
    meta = {"coletado_em": agora.isoformat(), "desde": desde, "canal": CANAL}
    gravar_json(bruto, {"meta": meta, "conversas": conversas})
    gravar_json(pre, {"meta": meta, "conversas": preparar(conversas)})
    nomes.write_text("\n".join(nomes_de(conversas, extras)) + "\n", encoding="utf-8")
    print(resumo(conversas, descartes, cli.chamadas))
    print(f"gravado (gitignored): {bruto.name}, {pre.name}, {nomes.name} em {args.saida}")
    return 0


if __name__ == "__main__":
    _reconfigurar = getattr(sys.stdout, "reconfigure", None)
    if _reconfigurar is not None:
        _reconfigurar(encoding="utf-8")
    sys.exit(main())
