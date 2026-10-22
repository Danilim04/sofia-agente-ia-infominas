# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Monta o dataset candidato de classificação a partir das conversas anonimizadas.

    # 1. sem JEV (não chama nada de rede): casos com `esperado: null`, `rotulador: "pendente"`
    python -m evals.pre_rotular
    # 2. depois, pré-rótulo com o JEV real (hipótese a revisar por humano, não gabarito)
    set -a && . ./.env && set +a && export TYPESAFE_API_KEY="$JEV_API_TOKEN"
    python -m evals.pre_rotular --jev --taxonomia ../descoberta-infodesk/taxonomia.rascunho.yaml

Entrada: `evals/bruto/conversas-whatsapp-anon.jsonl` (saída de `evals.anonimizar` sobre o
`-pre.json` do coletor). Saída: `evals/bruto/historico.candidato.jsonl`, um caso por conversa
(as `--limite` mais recentes): `estado` = mensagens anonimizadas até a primeira fala relevante
do cliente (≥ 4 palavras depois de tirar a saudação), no formato `Cliente:`/`Sofia:`.
Com `--jev` o caso ganha `esperado` (intenção, categoria, `prioridade_min`), `confianca` e
`rotulador: "jev-prelabel"`; a chave `confianca` não existe em `validar_caso` e sai na promoção
para `classificacao/historico.jsonl`. Só imprime agregados.
"""

from __future__ import annotations

import argparse
import asyncio
import json
import re
import sys
from collections import Counter
from collections.abc import Sequence
from pathlib import Path
from typing import Any, Literal

Autor = Literal["cliente", "sofia"]
BRUTO = Path(__file__).resolve().parent / "bruto"
ENTRADA = BRUTO / "conversas-whatsapp-anon.jsonl"
SAIDA = BRUTO / "historico.candidato.jsonl"
MAX_MSGS_ESTADO = 6
_LIDER = re.compile(r"^\W*(oi|ol[aá]|bom dia|boa tarde|boa noite|opa|tudo bem\??)[\s,.!?-]*", re.I)
_SO_SAUDACAO = re.compile(
    r"^\W*(oi|ol[aá]|bom dia|boa tarde|boa noite|opa|e a[ií]|tudo bem|td bem)\W*(tudo bem\W*)?$",
    re.I,
)


def primeira_fala_relevante(msgs: Sequence[dict[str, Any]]) -> int | None:
    """Índice da 1ª mensagem de texto do cliente com ≥ 4 palavras fora a saudação."""
    for i, m in enumerate(msgs):
        if m["sender"] != "CLIENT" or m["type"] != "TEXT":
            continue
        texto = _LIDER.sub("", m["body"].strip(), count=2)
        if len(texto.split()) >= 4 and not _SO_SAUDACAO.match(texto):
            return i
    return None


def linhas_do_estado(msgs: Sequence[dict[str, Any]], fim: int) -> list[tuple[Autor, str]]:
    """(autor, texto) das últimas mensagens de texto até `fim`; atendente humano conta como Sofia."""
    saida: list[tuple[Autor, str]] = []
    for m in msgs[: fim + 1][-MAX_MSGS_ESTADO:]:
        if m["type"] != "TEXT" or not m["body"].strip() or m["sender"] == "SYSTEM":
            continue
        autor: Autor = "cliente" if m["sender"] == "CLIENT" else "sofia"
        saida.append((autor, m["body"].strip()))
    return saida


def caso_pendente(
    n: int, conversa: dict[str, Any], linhas: list[tuple[Autor, str]]
) -> dict[str, Any]:
    return {
        "id": f"hist-{n:04d}",
        "fonte": "historico",
        "estado": [("Cliente: " if a == "cliente" else "Sofia: ") + t for a, t in linhas],
        "esperado": None,
        "notas": (
            f"conversa {conversa['id']}; departamento={'sim' if conversa['departmentId'] else 'não'}; "
            f"ticket={'sim' if conversa['ticketNumber'] is not None else 'não'}"
        ),
        "rotulador": "pendente",
    }


def selecionar(entrada: Path, limite: int) -> list[tuple[dict[str, Any], int]]:
    convs = [json.loads(x) for x in entrada.open(encoding="utf-8") if x.strip()]
    achadas = [(c, i) for c in convs if (i := primeira_fala_relevante(c["mensagens"])) is not None]
    achadas.sort(key=lambda p: p[0]["mensagens"][0]["createdAt"], reverse=True)
    return achadas[:limite]


async def rotular_com_jev(
    casos: list[dict[str, Any]], estados: list[list[tuple[Autor, str]]], taxonomia: Path
) -> int:
    """Preenche `esperado`/`confianca`/`rotulador` via JEV real. Devolve quantas falharam."""
    from src.classificacao.config import ConfigClassificacao
    from src.classificacao.jev import JEVTypeSafe, estado_para_jev
    from src.classificacao.perguntas import CATEGORIA, INTENCAO, PRIORIDADE, montar_perguntas
    from src.classificacao.taxonomia import carregar_taxonomia
    from src.classificacao.tipos import PRIORIDADES, Mensagem

    cfg = ConfigClassificacao()  # type: ignore[call-arg]
    if not cfg.typesafe_api_key:
        raise SystemExit("erro: exporte TYPESAFE_API_KEY (a partir de JEV_API_TOKEN).")
    perguntas = montar_perguntas(carregar_taxonomia(taxonomia))
    jev = JEVTypeSafe(cfg)
    sem = asyncio.Semaphore(4)
    falhas = 0

    async def um(caso: dict[str, Any], linhas: list[tuple[Autor, str]]) -> None:
        nonlocal falhas
        estado = estado_para_jev(
            [Mensagem(a, t) for a, t in linhas],
            janela=cfg.jev_janela_msgs,
            max_tokens=cfg.jev_max_tokens_estado,
            nomes=(),
        )
        async with sem:
            try:
                r = await jev.perguntar(estado, perguntas)
            except Exception as exc:
                falhas += 1
                print(f"  falha JEV: {type(exc).__name__}", file=sys.stderr)
                return
        ri, rc, rp = r.choices[INTENCAO], r.choices[CATEGORIA], r.scores[PRIORIDADE]
        caso["esperado"] = {
            "intencao": ri.escolha,
            "categoria": rc.escolha,
            "prioridade_min": PRIORIDADES[max(0, min(rp.nivel, len(PRIORIDADES) - 1))],
        }
        caso["confianca"] = {
            "intencao": round(ri.confianca, 3),
            "categoria": round(rc.confianca, 3),
            "prioridade": round(rp.confianca, 3),
        }
        caso["rotulador"] = "jev-prelabel"
        caso["notas"] = "HIPÓTESE do JEV a revisar por humano; " + str(caso["notas"])

    await asyncio.gather(*(um(c, e) for c, e in zip(casos, estados, strict=True)))
    return falhas


def resumo(casos: Sequence[dict[str, Any]]) -> str:
    rotulados = [c for c in casos if c.get("esperado")]
    linhas = [f"{len(casos)} caso(s); {len(rotulados)} com pré-rótulo do JEV"]
    if rotulados:
        for chave in ("intencao", "categoria", "prioridade_min"):
            dist = Counter(c["esperado"][chave] for c in rotulados).most_common()
            linhas.append(f"  {chave}: {dict(dist)}")
        for chave in ("intencao", "categoria", "prioridade"):
            media = sum(c["confianca"][chave] for c in rotulados) / len(rotulados)
            linhas.append(f"  confiança média {chave}: {media:.3f}")
    return "\n".join(linhas)


def main(argv: Sequence[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="python -m evals.pre_rotular", description=__doc__)
    p.add_argument("--entrada", type=Path, default=ENTRADA)
    p.add_argument("--saida", type=Path, default=SAIDA)
    p.add_argument("--limite", type=int, default=400, help="máximo de conversas (as mais recentes)")
    p.add_argument(
        "--jev", action="store_true", help="pré-rotula com o JEV real (precisa de chave)"
    )
    p.add_argument("--taxonomia", type=Path, help="taxonomia por área (com --jev)")
    args = p.parse_args(argv)
    if args.jev and args.taxonomia is None:
        p.error("--jev exige --taxonomia")
    if not args.entrada.exists():
        print(f"erro: entrada não existe: {args.entrada}")
        return 2
    achadas = selecionar(args.entrada, args.limite)
    estados = [linhas_do_estado(c["mensagens"], i) for c, i in achadas]
    casos = [
        caso_pendente(n, c, e)
        for n, ((c, _), e) in enumerate(zip(achadas, estados, strict=True), 1)
    ]
    falhas = 0
    if args.jev:
        falhas = asyncio.run(rotular_com_jev(casos, estados, args.taxonomia))
    args.saida.parent.mkdir(parents=True, exist_ok=True)
    with args.saida.open("w", encoding="utf-8", newline="\n") as f:
        for c in casos:
            f.write(json.dumps(c, ensure_ascii=False) + "\n")
    print(resumo(casos) + (f"; {falhas} falha(s) do JEV" if falhas else ""))
    print(f"gravado (gitignored): {args.saida}")
    return 0


if __name__ == "__main__":
    _reconfigurar = getattr(sys.stdout, "reconfigure", None)
    if _reconfigurar is not None:
        _reconfigurar(encoding="utf-8")
    sys.exit(main())
