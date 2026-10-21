#!/usr/bin/env python3
"""Agrega, só com contagens, como a Infominas usa o Infodesk hoje: tickets por
mesa × canal, uso do catálogo, conversas por canal/departamento e equipe por
departamento. Responde as perguntas de roteamento da taxonomia da Sofia
(docs/infodesk/10, §8) com números reais — ver a skill `analise-infodesk`.

Só faz `GET`. Stdlib pura (sem pip), como `descobrir-taxonomia.py`.

    set -a && . ./.env && set +a      # INFODESK_BASE_URL e INFODESK_API_KEY
    python scripts/analisar-infodesk.py --saida descoberta-infodesk/

A chave vem SEMPRE do ambiente. Saídas: `analise.json` (só agregados) e
`analise.md` (tabelas).

PRIVACIDADE: cada página é reduzida a uma lista branca de campos não pessoais
(`_ticket`, `_conversa`) e descartada em seguida. Nunca se grava nem se imprime
solicitante, telefone, contato, título, descrição, corpo, prévia de mensagem,
nome de pessoa nem id de ticket/conversa individual. Só entram contagens, meses
(`AAAA-MM`) e nomes de mesa, área, item de catálogo, departamento e canal; da
equipe, só quantos membros cada departamento tem (o `username` no Infominas é o
nome da pessoa: não sai). Ids de ticket/conversa existem apenas na memória do
processo, para ligar conversa → mesa, e não vão para nenhum arquivo.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request
from collections import Counter
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

PREFIXO = "/integration/v1"
PAUSA_S = 0.15  # rate limit do Desk: 300 req/min por IP (07, A4)
TIMEOUT_S = 30
TENTATIVAS = 6
ESPERA_429_S = 5.0  # dobra a cada tentativa; `Retry-After`, se vier, vence
CANAIS_CLIENTE = ("WHATSAPP", "CHAT")  # origem "cliente falou com a gente"
SEM = "(sem valor)"

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


# --------------------------------------------------------------------------- redução (lista branca)
def _nome(obj: Any) -> str:
    return " ".join(str((obj or {}).get("name") or "").split()) if isinstance(obj, dict) else ""


def _ticket(t: Json) -> Json:
    """Único ponto onde um ticket cru é lido: devolve só campos não pessoais."""
    status = t.get("status") if isinstance(t.get("status"), dict) else {}
    aberto = status.get("isOpen")
    if aberto is None:
        aberto = not t.get("isClosed", False)
    return {
        "mesa": _nome(t.get("desk")) or SEM,
        "canal": str(t.get("channel") or SEM),
        "mes": str(t.get("createdAt") or "")[:7] or SEM,
        "aberto": bool(aberto),
        "item": t.get("catalogItemId") or None,
        "tem_conversa": bool(t.get("conversationId")),
        # só na memória, para ligar conversa → mesa; nunca persistidos:
        "_numero": t.get("number"),
        "_conversa": t.get("conversationId") or None,
    }


def _conversa(c: Json) -> Json:
    return {
        "canal": str(c.get("channel") or SEM),
        "status": str(c.get("status") or SEM),
        "tratada_por": str(c.get("handledBy") or SEM),
        "bot_ativo": bool(c.get("botActive")),
        "departamento": c.get("departmentId") or None,
        "mes": str(c.get("createdAt") or "")[:7] or SEM,
        "tem_ticket": c.get("ticketNumber") is not None,
        "_numero": c.get("ticketNumber"),
        "_id": c.get("id") or None,
    }


def norm(texto: str) -> str:
    sem_acento = unicodedata.normalize("NFKD", texto).encode("ascii", "ignore").decode()
    return " ".join(sem_acento.lower().split())


def eh_casca(mesa: str) -> bool:
    """Mesas 'Incidente|Solicitações Externo|Interno|Remoto' (09: sem status nem prioridade)."""
    n = norm(mesa)
    return n.split(" ")[0] in ("incidente", "solicitacoes") and n.endswith(
        (" externo", " interno", " remoto")
    )


# --------------------------------------------------------------------------- coleta
def mes_limite(meses: int, agora: datetime) -> str:
    """Primeiro mês (AAAA-MM) da janela: `meses` meses completos antes do atual."""
    total = agora.year * 12 + (agora.month - 1) - meses
    return f"{total // 12:04d}-{total % 12 + 1:02d}"


class Paginador:
    """Percorre `{data, nextCursor}`; para cedo só se a lista vier em ordem
    decrescente de data (verificado página a página) e uma página inteira já for
    anterior ao início da janela."""

    def __init__(self, cli: Cliente, rota: str, query: dict[str, str], limite_paginas: int) -> None:
        self.cli, self.rota, self.query, self.limite = cli, rota, query, limite_paginas
        self.paginas = 0
        self.parou_cedo = False

    def __iter__(self) -> Any:
        cursor, vistos = "", set()
        while True:
            q = dict(self.query)
            if cursor:
                q["cursor"] = cursor
            dados = self.cli.get(self.rota, **q)
            self.paginas += 1
            yield itens(dados)
            cursor = proximo_cursor(dados)
            if not cursor or cursor in vistos:
                return
            vistos.add(cursor)
            if self.limite and self.paginas >= self.limite:
                self.parou_cedo = True
                return


def varrer(
    cli: Cliente,
    rota: str,
    query: dict[str, str],
    reduzir: Any,
    desde: str,
    limite_paginas: int,
    rotulo: str,
) -> tuple[list[Json], dict[str, Any]]:
    """Lê todas as páginas, reduz cada item pela lista branca e descarta o cru."""
    pag = Paginador(cli, rota, query, limite_paginas)
    out: list[Json] = []
    fora = 0
    desc = True
    menor_anterior = ""
    parou_por_data = False
    for pagina in pag:
        meses_pagina = [str(i.get("createdAt") or "")[:7] for i in pagina]
        for i in pagina:
            r = reduzir(i)
            if r["mes"] >= desde:
                out.append(r)
            else:
                fora += 1
        validos = [m for m in meses_pagina if m]
        if validos:
            if menor_anterior and max(validos) > menor_anterior:
                desc = False
            menor_anterior = min(validos)
            if desc and max(validos) < desde:
                parou_por_data = True
                break
        del pagina
        if pag.paginas % 10 == 0:
            print(f"  {rotulo}: {pag.paginas} página(s), {len(out)} na janela", file=sys.stderr)
    print(
        f"{rotulo}: {pag.paginas} página(s), {len(out)} na janela, {fora} antes da janela"
        + (" (parou: ordem decrescente e janela esgotada)" if parou_por_data else "")
        + (" (parou: --limite-paginas)" if pag.parou_cedo else ""),
        file=sys.stderr,
    )
    return out, {
        "paginas": pag.paginas,
        "fora_da_janela_lidos": fora,
        "ordem_decrescente": desc,
        "parou_por_data": parou_por_data,
        "parou_por_limite": pag.parou_cedo,
    }


def coletar(cli: Cliente, meses: int, limite_paginas: int, log: Any) -> Json:
    agora = datetime.now(UTC)
    desde = mes_limite(meses, agora)
    log(f"janela: tickets/conversas criados a partir de {desde} ({meses} meses completos + atual)")

    resumo_mesas = itens(cli.get("/desks"))
    mesas: list[Json] = []
    for m in resumo_mesas:
        det = cli.get(f"/desks/{m['id']}")
        mesas.append(det if isinstance(det, dict) else dict(m))
    log(f"{len(mesas)} mesa(s)")

    catalogo = {i["id"]: i for i in itens(cli.get("/catalog")) if i.get("id")}
    for m in mesas:
        for i in m.get("catalogItems") or []:
            catalogo.setdefault(i["id"], i)
    deps = itens(cli.get("/departments"))
    usuarios = itens(cli.get("/users"))
    dep_nome = {d["id"]: _nome(d) or SEM for d in deps}
    log(
        f"{len(catalogo)} itens de catálogo, {len(deps)} departamento(s), {len(usuarios)} usuário(s)"
    )

    tickets, meta_t = varrer(
        cli,
        "/tickets",
        {"status": "all", "limit": "100"},
        _ticket,
        desde,
        limite_paginas,
        "tickets",
    )
    num_mesa = {t["_numero"]: t["mesa"] for t in tickets if t["_numero"] is not None}
    conv_mesa = {t["_conversa"]: t["mesa"] for t in tickets if t["_conversa"]}

    conversas, meta_c = varrer(
        cli,
        "/conversations",
        {"tab": "todas", "limit": "100"},
        _conversa,
        desde,
        limite_paginas,
        "conversas",
    )

    a = agregar_tickets(tickets, mesas, catalogo)
    c = agregar_conversas(conversas, dep_nome, num_mesa, conv_mesa)
    equipe = agregar_equipe(usuarios, dep_nome)
    avisos = []
    if meta_t["parou_por_limite"] or meta_c["parou_por_limite"]:
        avisos.append("--limite-paginas truncou a leitura: números parciais, só para teste")
    if not meta_t["ordem_decrescente"]:
        avisos.append("tickets não vêm em ordem decrescente de data: todas as páginas foram lidas")
    if not meta_c["ordem_decrescente"]:
        avisos.append(
            "conversas não vêm em ordem decrescente de data: todas as páginas foram lidas"
        )
    return {
        "gerado_em": agora.isoformat(timespec="seconds"),
        "janela": {"meses": meses, "desde_mes": desde, "ate_mes": agora.strftime("%Y-%m")},
        "leitura": {"tickets": meta_t, "conversas": meta_c, "chamadas_http": cli.chamadas},
        "tickets": a,
        "conversas": c,
        "equipe_por_departamento": equipe,
        "avisos": avisos,
    }


# --------------------------------------------------------------------------- agregação
def agregar_tickets(tickets: list[Json], mesas: list[Json], catalogo: dict[str, Json]) -> Json:
    total, abertos, com_cat, com_conv = Counter(), Counter(), Counter(), Counter()
    por_mes: Counter[tuple[str, str, str]] = Counter()
    por_item: Counter[tuple[str, str, str]] = Counter()
    for t in tickets:
        k = (t["mesa"], t["canal"])
        total[k] += 1
        abertos[k] += t["aberto"]
        com_cat[k] += t["item"] is not None
        com_conv[k] += t["tem_conversa"]
        por_mes[(*k, t["mes"])] += 1
        if t["item"]:
            por_item[(t["mesa"], t["item"], t["canal"])] += 1

    nomes_mesas = [m["name"] for m in mesas]
    for mesa, _canal in total:  # mesas inativas ainda aparecem se tiverem ticket
        if mesa not in nomes_mesas:
            nomes_mesas.append(mesa)
    canais = sorted({c for _m, c in total})

    mesa_canal: Json = {}
    for mesa in nomes_mesas:
        mesa_canal[mesa] = {
            c: {
                "total": total[(mesa, c)],
                "abertos": abertos[(mesa, c)],
                "com_catalogo": com_cat[(mesa, c)],
                "com_conversa": com_conv[(mesa, c)],
                "por_mes": {
                    mes: n for (m2, c2, mes), n in sorted(por_mes.items()) if (m2, c2) == (mesa, c)
                },
            }
            for c in canais
            if total[(mesa, c)]
        }

    # Itens: por mesa, todos os itens do catálogo ATUAL da mesa (inclusive os com zero) e
    # também os usados em tickets da mesa que vieram de outro catálogo (migração X / X_New).
    linhas_itens: list[Json] = []
    for m in mesas:
        ids_mesa = {i["id"] for i in m.get("catalogItems") or []}
        for i in m.get("catalogItems") or []:
            linhas_itens.append(_linha_item(m["name"], i, por_item, canais, True))
        usados = {iid for (mesa, iid, _c) in por_item if mesa == m["name"]}
        for iid in sorted(usados - ids_mesa):
            item = catalogo.get(iid, {"name": "(item fora do catálogo da org)"})
            linhas_itens.append(_linha_item(m["name"], item, por_item, canais, False, iid))
    return {
        "total": len(tickets),
        "canais": canais,
        "mesa_x_canal": mesa_canal,
        "mesas_casca": sorted(n for n in nomes_mesas if eh_casca(n)),
        "itens": linhas_itens,
    }


def _linha_item(
    mesa: str,
    item: Json,
    por_item: Counter,
    canais: list[str],
    no_catalogo_atual: bool,
    iid: str | None = None,
) -> Json:
    iid = iid or item.get("id")
    por_canal = {c: por_item[(mesa, iid, c)] for c in canais if por_item[(mesa, iid, c)]}
    return {
        "mesa": mesa,
        "no_catalogo_atual": no_catalogo_atual,
        "area": _nome(item.get("area")) or "(sem área)",
        "item": " ".join(str(item.get("name") or "").split()) or SEM,
        "tipo": item.get("ticketType") or "(sem tipo)",
        "total": sum(por_canal.values()),
        "por_canal": por_canal,
    }


def agregar_conversas(
    conversas: list[Json], dep_nome: dict[str, str], num_mesa: dict, conv_mesa: dict
) -> Json:
    canal_status: Counter[tuple[str, str, str]] = Counter()
    por_dep: dict[str, Counter] = {}
    dep_mesa: Counter[tuple[str, str]] = Counter()
    sem_ticket_na_janela = 0
    for c in conversas:
        canal_status[(c["canal"], c["status"], c["tratada_por"])] += 1
        dep = dep_nome.get(
            c["departamento"], SEM if not c["departamento"] else "(departamento inativo)"
        )
        d = por_dep.setdefault(dep, Counter())
        d["total"] += 1
        d[f"canal:{c['canal']}"] += 1
        d[f"tratada_por:{c['tratada_por']}"] += 1
        d[f"status:{c['status']}"] += 1
        if c["tem_ticket"]:
            d["com_ticket"] += 1
        mesa = num_mesa.get(c["_numero"]) if c["_numero"] is not None else None
        mesa = mesa or conv_mesa.get(c["_id"])
        if mesa:
            dep_mesa[(dep, mesa)] += 1
            d["com_ticket_mesa_conhecida"] += 1
        elif c["tem_ticket"]:
            sem_ticket_na_janela += 1
    return {
        "total": len(conversas),
        "canal_x_status_x_tratada_por": [
            {"canal": ca, "status": st, "tratada_por": hb, "total": n}
            for (ca, st, hb), n in sorted(canal_status.items())
        ],
        "por_departamento": {k: dict(sorted(v.items())) for k, v in sorted(por_dep.items())},
        "departamento_x_mesa_do_ticket": [
            {"departamento": d, "mesa": m, "conversas": n}
            for (d, m), n in sorted(dep_mesa.items(), key=lambda kv: (kv[0][0], -kv[1]))
        ],
        "com_ticket_fora_da_janela_de_tickets": sem_ticket_na_janela,
    }


def agregar_equipe(usuarios: list[Json], dep_nome: dict[str, str]) -> Json:
    membros: Counter[str] = Counter()
    tipos: Counter[str] = Counter()
    for u in usuarios:
        tipos[str(u.get("type") or SEM)] += 1
        ids = u.get("departmentIds") or []
        for d in ids or [None]:
            membros[dep_nome.get(d, SEM) if d else "(sem departamento)"] += 1
    return {
        "usuarios_por_tipo": dict(tipos),
        "membros_por_departamento": dict(sorted(membros.items())),
    }


# --------------------------------------------------------------------------- markdown
def pct(parte: int, todo: int) -> str:
    return f"{100 * parte / todo:.0f}%" if todo else "—"


def tabela(cab: list[str], linhas: list[list[Any]]) -> list[str]:
    if not linhas:
        return ["(sem dados)", ""]
    out = ["| " + " | ".join(cab) + " |", "|" + "---|" * len(cab)]
    out += ["| " + " | ".join(str(x).replace("|", "/") for x in ln) + " |" for ln in linhas]
    return [*out, ""]


def markdown(a: Json) -> str:
    t, c, j = a["tickets"], a["conversas"], a["janela"]
    canais = t["canais"]
    mc: Json = t["mesa_x_canal"]
    out = [
        "# Análise do Infodesk (só agregados)",
        "",
        f"Gerado em {a['gerado_em']}. Janela: tickets e conversas criados de **{j['desde_mes']}** a "
        f"**{j['ate_mes']}** ({j['meses']} meses completos + o atual). "
        f"{t['total']} tickets e {c['total']} conversas na janela; "
        f"{a['leitura']['chamadas_http']} chamadas `GET`.",
        "",
        "Sem dado pessoal: só contagens e nomes de mesa/área/item/departamento/canal.",
        "",
    ]
    out += [f"> AVISO: {x}" for x in a.get("avisos", [])] + ([""] if a.get("avisos") else [])

    out += ["## 1. Tickets por mesa × canal (total / abertos) — Q1, Q3", ""]
    linhas = []
    for mesa, por_canal in mc.items():
        tot = sum(v["total"] for v in por_canal.values())
        linhas.append(
            [mesa, tot]
            + [
                f"{por_canal[ca]['total']} / {por_canal[ca]['abertos']}" if ca in por_canal else "0"
                for ca in canais
            ]
        )
    out += tabela(["Mesa", "Total", *canais], linhas)

    out += ["## 2. Mesas “Externo / Interno / Remoto” (esperado: zero) — Q1", ""]
    casca = t["mesas_casca"]
    out += tabela(
        ["Mesa", "Tickets na janela"],
        [[m, sum(v["total"] for v in mc.get(m, {}).values())] for m in casca],
    )

    out += ["## 3. Tickets por mês × mesa (todos os canais / só WHATSAPP+CHAT) — Q1, Q3", ""]
    meses = sorted({mes for pc in mc.values() for v in pc.values() for mes in v["por_mes"]})
    linhas = []
    for mesa, pc in mc.items():
        if not pc:
            continue
        cel = []
        for mes in meses:
            todos = sum(v["por_mes"].get(mes, 0) for v in pc.values())
            cli = sum(pc[ca]["por_mes"].get(mes, 0) for ca in CANAIS_CLIENTE if ca in pc)
            cel.append(f"{todos} / {cli}")
        linhas.append([mesa, *cel])
    out += tabela(["Mesa", *meses], linhas)

    out += ["## 4. Tickets com `catalogItemId` preenchido, por mesa × canal — Q6", ""]
    linhas = []
    for mesa, pc in mc.items():
        if not pc:
            continue
        tot = sum(v["total"] for v in pc.values())
        cat = sum(v["com_catalogo"] for v in pc.values())
        linhas.append(
            [mesa, f"{cat}/{tot} ({pct(cat, tot)})"]
            + [
                f"{pc[ca]['com_catalogo']}/{pc[ca]['total']} ({pct(pc[ca]['com_catalogo'], pc[ca]['total'])})"
                if ca in pc
                else "—"
                for ca in canais
            ]
        )
    out += tabela(["Mesa", "Todos", *canais], linhas)

    out += [
        "## 5. Tickets que nasceram de conversa (`conversationId`), por mesa × canal — Q3, Q4",
        "",
    ]
    linhas = []
    for mesa, pc in mc.items():
        if not pc:
            continue
        linhas.append(
            [mesa]
            + [
                f"{pc[ca]['com_conversa']}/{pc[ca]['total']} ({pct(pc[ca]['com_conversa'], pc[ca]['total'])})"
                if ca in pc
                else "—"
                for ca in canais
            ]
        )
    out += tabela(["Mesa", *canais], linhas)

    out += ["## 6. Financeiro por canal (total / abertos / com conversa) — Q2", ""]
    fin = {m: pc for m, pc in mc.items() if norm(m) == "financeiro"}
    out += tabela(
        ["Mesa", "Canal", "Total", "Abertos", "Com conversa", "Com catálogo"],
        [
            [m, ca, v["total"], v["abertos"], v["com_conversa"], v["com_catalogo"]]
            for m, pc in fin.items()
            for ca, v in pc.items()
        ],
    )

    out += [
        "## 7. Áreas do catálogo de Incidente / Solicitações em tickets de cliente (WHATSAPP+CHAT) — Q5",
        "",
        "“Itens no catálogo atual” = itens que `GET /desks/:id` lista hoje; “itens em uso” = itens "
        "(do catálogo atual ou de outro catálogo, p.ex. versão anterior) com ao menos um ticket de "
        "cliente na janela. Itens individuais na seção 8.",
        "",
    ]
    por_area: dict[tuple[str, str], dict[str, int]] = {}
    for it in t["itens"]:
        if norm(it["mesa"]) not in ("incidente", "solicitacoes"):
            continue
        d = por_area.setdefault(
            (it["mesa"], it["area"]),
            {"atual": 0, "usados": 0, "cli": 0, "cli_fora": 0, "todos": 0},
        )
        cli = sum(it["por_canal"].get(ca, 0) for ca in CANAIS_CLIENTE)
        d["atual"] += it["no_catalogo_atual"]
        d["usados"] += cli > 0
        d["cli"] += cli
        d["cli_fora"] += 0 if it["no_catalogo_atual"] else cli
        d["todos"] += it["total"]
    out += tabela(
        [
            "Mesa",
            "Área",
            "Itens no catálogo atual",
            "Itens em uso por cliente",
            "Tickets WHATSAPP+CHAT",
            "…dos quais em item de outro catálogo",
            "Tickets (todos os canais)",
        ],
        [
            [m, ar, d["atual"], d["usados"], d["cli"], d["cli_fora"], d["todos"]]
            for (m, ar), d in sorted(
                por_area.items(), key=lambda kv: (kv[0][0], -kv[1]["cli"], kv[0][1])
            )
        ],
    )

    out += ["## 8. Itens de catálogo (Incidente / Solicitações): tickets por canal — Q5", ""]
    linhas = [
        [
            it["mesa"],
            it["area"],
            it["item"],
            "sim" if it["no_catalogo_atual"] else "NÃO (outro catálogo)",
            it["total"],
        ]
        + [it["por_canal"].get(ca, 0) for ca in canais]
        for it in sorted(
            (i for i in t["itens"] if norm(i["mesa"]) in ("incidente", "solicitacoes")),
            key=lambda i: (i["mesa"], i["area"], -i["total"], i["item"]),
        )
    ]
    out += tabela(["Mesa", "Área", "Item", "No catálogo atual", "Total", *canais], linhas)

    out += ["## 9. Conversas por canal × status × quem trata — Q4", ""]
    out += tabela(
        ["Canal", "Status", "Tratada por", "Conversas"],
        [
            [x["canal"], x["status"], x["tratada_por"], x["total"]]
            for x in c["canal_x_status_x_tratada_por"]
        ],
    )

    out += ["## 10. Conversas por departamento — Q4", ""]
    pd = c["por_departamento"]
    canais_c = sorted({k[6:] for v in pd.values() for k in v if k.startswith("canal:")})
    tratas = sorted({k[12:] for v in pd.values() for k in v if k.startswith("tratada_por:")})
    out += tabela(
        [
            "Departamento",
            "Total",
            "Com ticket",
            *[f"canal {x}" for x in canais_c],
            *[f"por {x}" for x in tratas],
        ],
        [
            [
                d,
                v.get("total", 0),
                f"{v.get('com_ticket', 0)} ({pct(v.get('com_ticket', 0), v.get('total', 0))})",
            ]
            + [v.get(f"canal:{x}", 0) for x in canais_c]
            + [v.get(f"tratada_por:{x}", 0) for x in tratas]
            for d, v in pd.items()
        ],
    )

    out += ["## 11. Departamento da conversa → mesa do ticket aberto a partir dela — Q4", ""]
    out += tabela(
        ["Departamento", "Mesa do ticket", "Conversas"],
        [
            [x["departamento"], x["mesa"], x["conversas"]]
            for x in c["departamento_x_mesa_do_ticket"]
        ],
    )
    if c["com_ticket_fora_da_janela_de_tickets"]:
        out += [
            f"{c['com_ticket_fora_da_janela_de_tickets']} conversa(s) têm ticket criado antes da "
            "janela de tickets (mesa desconhecida).",
            "",
        ]

    out += ["## 12. Membros por departamento (`GET /users`) — Q4", ""]
    eq = a["equipe_por_departamento"]
    out += tabela(
        ["Departamento", "Membros"],
        [[d, n] for d, n in eq["membros_por_departamento"].items()],
    )
    out += [
        "Tipos de usuário: " + ", ".join(f"{k}={v}" for k, v in eq["usuarios_por_tipo"].items()),
        "",
    ]
    return "\n".join(out)


# --------------------------------------------------------------------------- main
def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    p.add_argument(
        "--saida",
        default="descoberta-infodesk",
        help="pasta de saída (default: descoberta-infodesk/)",
    )
    p.add_argument(
        "--base-url",
        default=os.environ.get("INFODESK_BASE_URL", ""),
        help="default: $INFODESK_BASE_URL",
    )
    p.add_argument(
        "--meses", type=int, default=12, help="janela em meses completos + o atual (default: 12)"
    )
    p.add_argument(
        "--limite-paginas",
        type=int,
        default=0,
        help="máx. de páginas por rota (0 = sem limite; só para teste)",
    )
    p.add_argument(
        "--de-arquivo",
        default="",
        help="regenera o analise.md a partir de um analise.json, sem chamar a API",
    )
    args = p.parse_args(argv)
    sys.stderr.reconfigure(encoding="utf-8")  # console do Windows (cp1252) quebra acentos

    def log(msg: str) -> None:
        print(msg, file=sys.stderr)

    saida = Path(args.saida)
    saida.mkdir(parents=True, exist_ok=True)
    if args.de_arquivo:
        analise = json.loads(Path(args.de_arquivo).read_text(encoding="utf-8"))
        log(f"analise lida de {args.de_arquivo}")
    else:
        chave = os.environ.get("INFODESK_API_KEY", "").strip()
        if not args.base_url.strip() or not chave:
            print(
                "defina INFODESK_BASE_URL e INFODESK_API_KEY no ambiente (a chave nunca vai por argumento)",
                file=sys.stderr,
            )
            return 2
        if args.meses < 1:
            print("--meses deve ser >= 1", file=sys.stderr)
            return 2
        try:
            analise = coletar(Cliente(args.base_url, chave), args.meses, args.limite_paginas, log)
        except ErroAPI as exc:
            print(f"falha: {exc}", file=sys.stderr)
            return 1
        (saida / "analise.json").write_text(
            json.dumps(analise, ensure_ascii=False, indent=2), encoding="utf-8"
        )
    (saida / "analise.md").write_text(markdown(analise), encoding="utf-8")
    log(f"gravado em {saida}/: analise.md" + ("" if args.de_arquivo else ", analise.json"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
