#!/usr/bin/env python3
"""Descobre mesas, prioridades, catálogo e departamentos do Infodesk e gera um
rascunho de `config/taxonomia.yaml` (docs/infodesk/10, §7; pendências P1, P4,
P11 e item D6 do 07).

Só faz `GET`. Stdlib pura (sem pip), como o mock-infodesk.

    INFODESK_BASE_URL=https://infodesk.infominas.net/v1 \\
    INFODESK_API_KEY=ik_live_... \\
    python scripts/descobrir-taxonomia.py --saida descoberta-infodesk/

A chave vem SEMPRE do ambiente (nunca por argumento, para não ficar no
histórico do shell). Saídas: `inventario.json` (cru), `inventario.md`
(tabelas) e `taxonomia.rascunho.yaml` (rascunho com TODOs para a Infominas).
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

PREFIXO = "/integration/v1"
PAUSA_S = 0.1  # rate limit do Desk: 300 req/min por IP (07, A4)
TIMEOUT_S = 20

# Nomes canônicos da Sofia → nome mais provável no Infodesk (confirmar por mesa).
PRIORIDADES_CANONICAS = {"baixa": "Baixa", "media": "Média", "alta": "Alta", "urgente": "Urgente"}

Json = dict[str, Any]


# --------------------------------------------------------------------------- HTTP
class ErroAPI(Exception):
    def __init__(self, status: int, corpo: str, rota: str) -> None:
        super().__init__(f"{rota}: HTTP {status}: {corpo[:300]}")
        self.status = status


class Cliente:
    def __init__(self, base_url: str, api_key: str) -> None:
        base = base_url.strip().rstrip("/")
        if not base.endswith(PREFIXO):
            base += PREFIXO
        self.base = base
        self.api_key = api_key

    def get(self, rota: str, **query: str) -> Any:
        url = self.base + rota
        if query:
            url += "?" + urllib.parse.urlencode(query)
        req = urllib.request.Request(  # noqa: S310 — só http(s), URL vem do operador
            url,
            headers={"Authorization": f"Bearer {self.api_key}", "Accept": "application/json"},
            method="GET",
        )
        try:
            with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:  # noqa: S310
                dados = json.loads(resp.read().decode("utf-8") or "null")
        except urllib.error.HTTPError as exc:
            raise ErroAPI(exc.code, exc.read().decode("utf-8", "replace"), rota) from None
        except urllib.error.URLError as exc:
            raise ErroAPI(0, str(exc.reason), rota) from None
        time.sleep(PAUSA_S)
        return dados


def lista(dados: Any) -> list[Json]:
    """Aceita array puro ou `{data: [...]}` (07, D3: o código devolve array)."""
    if isinstance(dados, dict) and isinstance(dados.get("data"), list):
        return dados["data"]
    if isinstance(dados, list):
        return dados
    return []


# --------------------------------------------------------------------------- coleta
def coletar(cli: Cliente, log: Any) -> Json:
    me = cli.get("/me")
    log(
        f"chave '{me.get('name')}' da org {me.get('organizationId')} — escopos: "
        f"{', '.join(me.get('scopes') or []) or '(nenhum)'}; conduz o atendimento: "
        f"{'sim' if me.get('handlesConversations') else 'NÃO'}"
    )
    mesas_resumo = lista(cli.get("/desks"))
    log(f"{len(mesas_resumo)} mesa(s) ativa(s)")
    mesas: list[Json] = []
    for m in mesas_resumo:
        detalhe = cli.get(f"/desks/{m['id']}")
        mesas.append(detalhe if isinstance(detalhe, dict) else dict(m))
        log(
            f"  mesa '{m.get('name')}': {len(detalhe.get('priorities') or [])} prioridade(s), "
            f"{len(detalhe.get('statuses') or [])} status, {len(detalhe.get('catalogItems') or [])} item(ns) de catálogo"
        )
    # Itens de catálogo: por mesa (`?deskId=`, que o mock e o Desk aceitam) e, se o Desk
    # permitir, a lista inteira sem `deskId` (código do Infodesk: todos os itens da org;
    # o mock responde 400). Dedup por id.
    catalogo: list[Json] = []
    vistos: set[str] = set()
    for m in mesas:
        for item in lista(cli.get("/catalog", deskId=m["id"])):
            if item.get("id") not in vistos:
                vistos.add(item.get("id"))
                catalogo.append(item)
    try:
        for item in lista(cli.get("/catalog")):
            if item.get("id") not in vistos:
                vistos.add(item.get("id"))
                catalogo.append(item)
    except ErroAPI as exc:
        if exc.status != 400:
            raise
        log("  GET /catalog sem deskId não aceito (mock); usando só o catálogo por mesa")
    log(f"{len(catalogo)} item(ns) de catálogo na organização")
    departamentos = lista(cli.get("/departments"))
    log(f"{len(departamentos)} departamento(s) de chat")
    if not departamentos:
        log("  AVISO: nenhum departamento de chat — o handoff não terá destino (07, D6; P11)")
    return {
        "coletado_em": datetime.now(UTC).isoformat(timespec="seconds"),
        "base_url": cli.base,
        "me": me,
        "desks": mesas,
        "catalog": catalogo,
        "departments": departamentos,
    }


# --------------------------------------------------------------------------- análise
def slug(texto: str) -> str:
    """Nome de categoria: snake_case, sem acento, sem espaço (regra de taxonomia.py)."""
    sem_acento = unicodedata.normalize("NFKD", texto).encode("ascii", "ignore").decode()
    s = re.sub(r"[^a-z0-9]+", "_", sem_acento.lower()).strip("_")
    return s or "sem_nome"


def matriz_prioridades(mesas: list[Json]) -> dict[str, dict[str, str | None]]:
    """canônica → {mesa → nome encontrado ou None}."""
    out: dict[str, dict[str, str | None]] = {c: {} for c in PRIORIDADES_CANONICAS}
    for m in mesas:
        nomes = {slug(p.get("name", "")): p.get("name") for p in m.get("priorities") or []}
        for canonica, esperado in PRIORIDADES_CANONICAS.items():
            out[canonica][m["name"]] = nomes.get(slug(esperado))
    return out


def mesa_do_item(item: Json, mesas: list[Json]) -> Json | None:
    """Mesa de um item: `defaultDeskId`; senão a (única) mesa cujo catálogo o contém."""
    por_id = {m["id"]: m for m in mesas}
    if item.get("defaultDeskId") in por_id:
        return por_id[item["defaultDeskId"]]
    donas = [
        m for m in mesas if any(i.get("id") == item.get("id") for i in m.get("catalogItems") or [])
    ]
    return donas[0] if len(donas) == 1 else None


def montar_rascunho(inv: Json) -> tuple[str, list[str]]:
    mesas: list[Json] = inv["desks"]
    catalogo: list[Json] = inv["catalog"]
    departamentos: list[Json] = inv["departments"]
    avisos: list[str] = []
    nomes_mesa = [m["name"] for m in mesas]

    triagem = next(
        (n for n in nomes_mesa if slug(n) == "abertura_de_ticket"),
        nomes_mesa[0] if nomes_mesa else "TODO",
    )
    dep_padrao = departamentos[0]["name"] if len(departamentos) == 1 else "TODO"

    matriz = matriz_prioridades(mesas)
    linhas: list[str] = [
        "# RASCUNHO gerado por scripts/descobrir-taxonomia.py em "
        f"{inv['coletado_em']} a partir de {inv['base_url']}",
        "# Formato: docs/plano/02-classificacao.md. Regras lidas no código: docs/infodesk/10.",
        "# Cada 'TODO' é decisão da Infominas (10, §8). Revisar as categorias contra o que o",
        "# cliente escreve no WhatsApp: item de catálogo é a visão do técnico, não a do cliente.",
        "placeholder: true   # remover só depois de preencher todos os TODO (E5.1)",
        f"versao: {datetime.now(UTC).date().isoformat()}",
        f'mesa_triagem: "{triagem}"   # TODO(P11): confirmar destino de "indefinida"',
        f'departamento_humano_padrao: "{dep_padrao}"   # TODO(P11): destino de toda transferência',
        "",
        "categorias:",
    ]
    usados: dict[str, int] = {}
    categorias = 0
    for item in catalogo:
        mesa = mesa_do_item(item, mesas)
        if mesa is None:
            avisos.append(
                f"item '{item.get('name')}' ({item.get('code') or item.get('id')}) sem mesa padrão: fora do rascunho"
            )
            continue
        nome = slug(item.get("name", ""))
        if nome in usados:
            usados[nome] += 1
            nome = f"{nome}_{usados[nome]}"
        else:
            usados[nome] = 1
        descricao = (item.get("description") or item.get("name") or "").strip().replace('"', "'")
        area = (item.get("area") or {}).get("name") or "—"
        tipo = item.get("ticketType") or "tipo —"
        linhas += [
            f"  {nome}:",
            f'    descricao: "{descricao}"   # TODO: reescrever na linguagem do cliente',
            '    exemplos: ["TODO frase real do cliente", "TODO outra"]',
            "    nao_e: []",
            f'    mesa: "{mesa["name"]}"   # {tipo}; área: {area}',
            f'    catalogo: "{item.get("name")}"   # catalogItemId {item.get("id")}',
            f'    departamento: "{dep_padrao}"   # TODO(D1): departamento do handoff',
            "    prioridade: media   # TODO: ver matriz prioridade × mesa em inventario.md",
            "",
        ]
        categorias += 1
    linhas += [
        "  indefinida:",
        '    descricao: "Não foi possível determinar: mensagem vaga, só saudação, mídia sem texto"',
        '    exemplos: ["oi", "preciso de ajuda", "tô com um problema"]',
        "    nao_e: []",
        f'    mesa: "{triagem}"',
        f'    departamento: "{dep_padrao}"',
        "    prioridade: media",
        "",
        "prioridades:   # nome canônico → NOME no Infodesk; o gateway resolve o UUID por mesa (02, P4)",
    ]
    for canonica, esperado in PRIORIDADES_CANONICAS.items():
        faltam = [m for m, achado in matriz[canonica].items() if achado is None]
        nota = "presente em todas as mesas" if not faltam else f"AUSENTE em: {', '.join(faltam)}"
        linhas.append(f'  {canonica}: "{esperado}"   # {nota}')
    if categorias == 0:
        avisos.append(
            "nenhum item de catálogo com mesa: o rascunho só tem 'indefinida' — mapear categorias à mão a partir das mesas"
        )
    return "\n".join(linhas) + "\n", avisos


def montar_markdown(inv: Json, avisos: list[str]) -> str:
    mesas: list[Json] = inv["desks"]
    me = inv["me"]
    out = [
        "# Inventário do Infodesk",
        "",
        f"Coletado em {inv['coletado_em']} de `{inv['base_url']}` com a chave "
        f"**{me.get('name')}** (org `{me.get('organizationId')}`; escopos: "
        f"{', '.join(me.get('scopes') or []) or 'nenhum'}; conduz o atendimento: "
        f"{'sim' if me.get('handlesConversations') else 'não'}).",
        "",
        "## Mesas ativas",
        "",
        "| Mesa | id | Prioridades | Status (abertos / fechados) | Etapas | Catálogo |",
        "|---|---|---|---|---|---|",
    ]
    for m in mesas:
        pr = ", ".join(p.get("name", "?") for p in m.get("priorities") or []) or "—"
        st = m.get("statuses") or []
        abertos = [s["name"] for s in st if s.get("isOpen")]
        fechados = [s["name"] for s in st if s.get("isClose") or s.get("isCanceled")]
        et = " → ".join(s.get("name", "?") for s in m.get("stages") or []) or "—"
        cat = (
            f"{len(m.get('catalogItems') or [])} item(ns)" if m.get("catalogEnabled") else "não usa"
        )
        out.append(
            f"| {m['name']} | `{m['id']}` | {pr} | {', '.join(abertos) or '—'} / {', '.join(fechados) or '—'} | {et} | {cat} |"
        )

    out += ["", "## Matriz prioridade × mesa (nome canônico da Sofia → nome encontrado)", ""]
    matriz = matriz_prioridades(mesas)
    out.append("| Canônica | " + " | ".join(m["name"] for m in mesas) + " |")
    out.append("|---|" + "---|" * len(mesas))
    for canonica in PRIORIDADES_CANONICAS:
        out.append(
            f"| {canonica} | "
            + " | ".join((matriz[canonica].get(m["name"]) or "**AUSENTE**") for m in mesas)
            + " |"
        )

    out += [
        "",
        "## Itens de catálogo",
        "",
        "| Item | Código | Tipo | Área | Mesa padrão | id |",
        "|---|---|---|---|---|---|",
    ]
    for item in inv["catalog"]:
        mesa = mesa_do_item(item, mesas)
        out.append(
            f"| {item.get('name')} | {item.get('code') or '—'} | {item.get('ticketType') or '—'} | "
            f"{(item.get('area') or {}).get('name') or '—'} | {mesa['name'] if mesa else '**sem mesa**'} | `{item.get('id')}` |"
        )

    out += [
        "",
        "## Departamentos de chat (destinos do handoff)",
        "",
        "| Departamento | Descrição | id |",
        "|---|---|---|",
    ]
    for d in inv["departments"]:
        out.append(f"| {d.get('name')} | {d.get('description') or '—'} | `{d.get('id')}` |")
    if not inv["departments"]:
        out.append("| (nenhum) | | |")

    if avisos:
        out += ["", "## Avisos", ""] + [f"- {a}" for a in avisos]
    out += [
        "",
        "## Próximos passos (E5.1)",
        "",
        "1. Responder o questionário de docs/infodesk/10 §8 com a Infominas.",
        "2. Revisar `taxonomia.rascunho.yaml` (categorias na linguagem do cliente, TODOs) e copiar para `agente-ia/config/taxonomia.yaml`.",
        "3. Remover `placeholder: true`; rodar `scripts/verificar.sh --rapido` e os evals de classificação.",
        "",
    ]
    return "\n".join(out)


# --------------------------------------------------------------------------- main
def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument(
        "--saida",
        default="descoberta-infodesk",
        help="pasta de saída (default: descoberta-infodesk/)",
    )
    parser.add_argument(
        "--base-url",
        default=os.environ.get("INFODESK_BASE_URL", ""),
        help="raiz da API (default: $INFODESK_BASE_URL)",
    )
    args = parser.parse_args(argv)

    api_key = os.environ.get("INFODESK_API_KEY", "").strip()
    if not args.base_url.strip() or not api_key:
        print(
            "defina INFODESK_BASE_URL e INFODESK_API_KEY no ambiente (a chave nunca vai por argumento)",
            file=sys.stderr,
        )
        return 2

    def log(msg: str) -> None:
        print(msg, file=sys.stderr)

    try:
        inv = coletar(Cliente(args.base_url, api_key), log)
    except ErroAPI as exc:
        dica = {
            401: "chave ausente/inválida/revogada",
            403: "chave sem escopo (dados de referência não exigem escopo; conferir a URL base)",
            404: "rota não encontrada: a base URL deve terminar em /v1 ou /v1/integration/v1 (07, A6)",
            429: "rate limit (300/min): aguarde e repita",
            0: "sem conexão",
        }.get(exc.status, "")
        print(f"falha: {exc}" + (f" — {dica}" if dica else ""), file=sys.stderr)
        return 1

    rascunho, avisos = montar_rascunho(inv)
    saida = Path(args.saida)
    saida.mkdir(parents=True, exist_ok=True)
    (saida / "inventario.json").write_text(
        json.dumps(inv, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    (saida / "inventario.md").write_text(montar_markdown(inv, avisos), encoding="utf-8")
    (saida / "taxonomia.rascunho.yaml").write_text(rascunho, encoding="utf-8")
    for a in avisos:
        log(f"AVISO: {a}")
    log(f"gravado em {saida}/: inventario.json, inventario.md, taxonomia.rascunho.yaml")
    return 0


if __name__ == "__main__":
    sys.exit(main())
