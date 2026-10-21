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
PRIORIDADES_CANONICAS: dict[str, list[str]] = {
    "baixa": ["Baixa"],
    "media": ["Média", "Media", "Normal"],
    "alta": ["Alta"],
    "urgente": ["Urgente", "Crítica", "Critica"],
}

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
        for canonica, aliases in PRIORIDADES_CANONICAS.items():
            out[canonica][m["name"]] = next(
                (nomes[slug(a)] for a in aliases if slug(a) in nomes), None
            )
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


def escolher_mesas(mesas: list[Json], pedidas: list[str], avisos: list[str]) -> list[Json]:
    """Mesas pedidas em `--mesas` (comparação sem acento/caixa); vazio = todas."""
    if not pedidas:
        return mesas
    por_slug = {slug(m["name"]): m for m in mesas}
    out: list[Json] = []
    for nome in pedidas:
        m = por_slug.get(slug(nome))
        if m is None:
            avisos.append(f"mesa '{nome}' não existe entre as ativas: {[x['name'] for x in mesas]}")
        else:
            out.append(m)
    return out


def _nome_unico(base: str, usados: dict[str, int]) -> str:
    if base in usados:
        usados[base] += 1
        return f"{base}_{usados[base]}"
    usados[base] = 1
    return base


def _sufixo_tipo(itens: list[Json]) -> str:
    tipos = {(i.get("ticketType") or "").upper() for i in itens}
    if tipos == {"INCIDENT"}:
        return "incidente"
    if tipos == {"REQUEST"}:
        return "solicitacao"
    return ""


def montar_rascunho(
    inv: Json, pedidas: list[str] | None = None, por: str = "area"
) -> tuple[str, list[str]]:
    """Rascunho de taxonomia.

    Sem `--mesas`: uma categoria por item de catálogo que tenha mesa inequívoca
    (`defaultDeskId` ou catálogo de uma mesa só). Com `--mesas`: só os catálogos
    dessas mesas, agrupados por `por` ("area" = uma categoria por área do catálogo,
    com os itens listados em comentário para escolher o `catalogo`; "item" = uma por
    item). O agrupamento por área é o ponto de partida realista: 10–20 categorias
    cabem numa pergunta do JEV e na cabeça do cliente; 80 itens não.
    """
    mesas: list[Json] = inv["desks"]
    catalogo: list[Json] = inv["catalog"]
    departamentos: list[Json] = inv["departments"]
    avisos: list[str] = []
    nomes_mesa = [m["name"] for m in mesas]
    escolhidas = escolher_mesas(mesas, pedidas or [], avisos)

    triagem = next(
        (n for n in nomes_mesa if slug(n) == "abertura_de_ticket"),
        nomes_mesa[0] if nomes_mesa else "TODO",
    )
    dep_padrao = departamentos[0]["name"] if len(departamentos) == 1 else "TODO"
    deps = ", ".join(d.get("name", "?") for d in departamentos) or "nenhum"

    matriz = matriz_prioridades(mesas)
    linhas: list[str] = [
        "# RASCUNHO gerado por scripts/descobrir-taxonomia.py em "
        f"{inv['coletado_em']} a partir de {inv['base_url']}",
        f"# Mesas: {', '.join(m['name'] for m in escolhidas) if pedidas else 'todas (sem --mesas)'}; agrupamento: {por if pedidas else 'item com mesa inequívoca'}.",
        "# Formato: docs/plano/02-classificacao.md. Regras lidas no código: docs/infodesk/10.",
        "# Cada 'TODO' é decisão da Infominas (10, §8). Revisar as categorias contra o que o",
        "# cliente escreve no WhatsApp: item de catálogo é a visão do técnico, não a do cliente.",
        "placeholder: true   # remover só depois de preencher todos os TODO (E5.1)",
        f"versao: {datetime.now(UTC).date().isoformat()}",
        f'mesa_triagem: "{triagem}"   # TODO(P11): confirmar destino de "indefinida"',
        f'departamento_humano_padrao: "{dep_padrao}"   # TODO(P11): destino de toda transferência; departamentos de chat: {deps}',
        "",
        "categorias:",
    ]
    usados: dict[str, int] = {}
    categorias = 0

    def bloco(
        nome: str, descricao: str, mesa: Json, itens: list[Json], area: str, comentario_extra: str
    ) -> list[str]:
        desc = descricao.strip().replace('"', "'")
        tipos = ", ".join(sorted({i.get("ticketType") or "—" for i in itens}))
        cat = (
            [f'    catalogo: "{itens[0].get("name")}"   # catalogItemId {itens[0].get("id")}']
            if len(itens) == 1
            else [
                "    # catalogo: OPCIONAL — escolher UM item abaixo (nome exato) ou omitir:",
                *[f"    #   - {i.get('name')}   # {i.get('id')}" for i in itens],
            ]
        )
        faltas = [c for c in PRIORIDADES_CANONICAS if matriz[c].get(mesa["name"]) is None]
        nota_pr = (
            f"nesta mesa faltam: {', '.join(faltas)}" if faltas else "todas existem nesta mesa"
        )
        return [
            f"  {nome}:",
            f'    descricao: "{desc}"   # TODO: reescrever na linguagem do cliente',
            '    exemplos: ["TODO frase real do cliente", "TODO outra"]',
            "    nao_e: []",
            f'    mesa: "{mesa["name"]}"   # {tipos}; área: {area}{comentario_extra}',
            *cat,
            f'    departamento: "{dep_padrao}"   # TODO(D1): departamento do handoff ({deps})',
            f"    prioridade: media   # TODO: {nota_pr} (matriz em inventario.md)",
            "",
        ]

    if pedidas:
        # Colisão de área entre mesas (ex.: "E-mail" em Incidente e em Solicitações): sufixo pelo tipo.
        contagem_area: dict[str, int] = {}
        for m in escolhidas:
            for a in {
                (i.get("area") or {}).get("name") or "sem_area" for i in m.get("catalogItems") or []
            }:
                contagem_area[slug(a)] = contagem_area.get(slug(a), 0) + 1
        for m in escolhidas:
            itens_mesa: list[Json] = m.get("catalogItems") or []
            if not itens_mesa:
                avisos.append(
                    f"mesa '{m['name']}' não usa catálogo: categorias dela devem ser escritas à mão"
                )
                continue
            if por == "item":
                for item in itens_mesa:
                    area = (item.get("area") or {}).get("name") or "—"
                    nome = _nome_unico(slug(item.get("name", "")), usados)
                    linhas += bloco(
                        nome, item.get("description") or item.get("name") or "", m, [item], area, ""
                    )
                    categorias += 1
                continue
            por_area: dict[str, list[Json]] = {}
            for item in itens_mesa:
                por_area.setdefault((item.get("area") or {}).get("name") or "sem_area", []).append(
                    item
                )
            for area, itens in por_area.items():
                base = slug(area)
                if contagem_area.get(base, 0) > 1:
                    base = f"{base}_{_sufixo_tipo(itens) or slug(m['name'])}"
                nome = _nome_unico(base, usados)
                descricao = (
                    f"{area}: "
                    + "; ".join(i.get("name", "") for i in itens[:6])
                    + (" …" if len(itens) > 6 else "")
                )
                linhas += bloco(nome, descricao, m, itens, area, f" ({len(itens)} itens)")
                categorias += 1
    else:
        for item in catalogo:
            mesa = mesa_do_item(item, mesas)
            if mesa is None:
                avisos.append(
                    f"item '{item.get('name')}' ({item.get('code') or item.get('id')}) sem mesa inequívoca: fora do rascunho (use --mesas)"
                )
                continue
            nome = _nome_unico(slug(item.get("name", "")), usados)
            area = (item.get("area") or {}).get("name") or "—"
            linhas += bloco(
                nome, item.get("description") or item.get("name") or "", mesa, [item], area, ""
            )
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
    alvo = escolhidas if pedidas else mesas
    for canonica, aliases in PRIORIDADES_CANONICAS.items():
        achados = [matriz[canonica].get(m["name"]) for m in alvo]
        nomes_achados = sorted({a for a in achados if a})
        faltam = [m["name"] for m, a in zip(alvo, achados, strict=True) if a is None]
        escolhido = nomes_achados[0] if len(nomes_achados) == 1 else aliases[0]
        nota = (
            f"grafias diferentes por mesa: {nomes_achados}; " if len(nomes_achados) > 1 else ""
        ) + (
            "presente em todas as mesas escolhidas"
            if not faltam
            else f"AUSENTE em: {', '.join(faltam)}"
        )
        linhas.append(f'  {canonica}: "{escolhido}"   # {nota}')
    if categorias == 0:
        avisos.append(
            "nenhuma categoria gerada: use --mesas com as mesas que a Sofia vai usar (ex.: --mesas Incidente,Solicitações)"
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
    parser.add_argument(
        "--mesas",
        default="",
        help='mesas que a Sofia vai usar, separadas por vírgula (ex.: "Incidente,Solicitações"); vazio = heurística por item',
    )
    parser.add_argument(
        "--por",
        choices=["area", "item"],
        default="area",
        help="com --mesas: uma categoria por área do catálogo (default) ou por item",
    )
    parser.add_argument(
        "--de-arquivo",
        default="",
        help="reusa um inventario.json já coletado em vez de chamar a API (só regenera o rascunho)",
    )
    args = parser.parse_args(argv)
    pedidas = [m.strip() for m in args.mesas.split(",") if m.strip()]

    def log(msg: str) -> None:
        print(msg, file=sys.stderr)

    if args.de_arquivo:
        inv = json.loads(Path(args.de_arquivo).read_text(encoding="utf-8"))
        log(f"inventário lido de {args.de_arquivo} (coletado em {inv.get('coletado_em')})")
    else:
        api_key = os.environ.get("INFODESK_API_KEY", "").strip()
        if not args.base_url.strip() or not api_key:
            print(
                "defina INFODESK_BASE_URL e INFODESK_API_KEY no ambiente (a chave nunca vai por argumento)",
                file=sys.stderr,
            )
            return 2
        inv = None
    try:
        if inv is None:
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

    rascunho, avisos = montar_rascunho(inv, pedidas, args.por)
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
