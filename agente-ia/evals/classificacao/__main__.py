# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""CLI: `python -m evals.classificacao --fonte sintetico|historico [--jev|--llm] [--relatorio caminho]`.

Saídas: relatório Markdown (default `evals/relatorios/classificacao-<fonte>-<modo>.md`)
e um `.json` ao lado com as métricas planas (0–100) para comparar com
`evals/baseline.json`. Código de saída: 0 ok; 1 regressão acima da tolerância
contra o baseline (quando `--baseline` tem números para `<fonte>/<modo>`);
2 dataset inválido ou argumento errado.
"""

from __future__ import annotations

import argparse
import json
import sys
from collections.abc import Callable, Sequence
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from evals.classificacao import (
    FONTES_ARQUIVO,
    LIMIARES_PADRAO,
    carregar_dataset,
    contar_por_categoria,
    obter_classificador,
    rodar,
)
from evals.formatos import CAMINHO_TAXONOMIA, ErroFormato, carregar_taxonomia
from evals.relatorio import comparar_com_baseline, gerar_relatorio

RAIZ_EVALS = Path(__file__).resolve().parent.parent
BASELINE_PADRAO = RAIZ_EVALS / "baseline.json"
RELATORIOS = RAIZ_EVALS / "relatorios"


def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="python -m evals.classificacao", description=__doc__)
    p.add_argument("--fonte", choices=sorted(FONTES_ARQUIVO), required=True)
    modo = p.add_mutually_exclusive_group()
    modo.add_argument(
        "--jev",
        action="store_const",
        const="jev",
        dest="modo",
        help="JEV real (TYPESAFE_API_KEY) — TODO(E2.2)",
    )
    modo.add_argument(
        "--llm",
        action="store_const",
        const="llm",
        dest="modo",
        help="fallback LLM/fakes, determinístico — TODO(E2.2)",
    )
    p.set_defaults(modo="llm")
    p.add_argument(
        "--relatorio", type=Path, default=None, help="caminho do .md (default evals/relatorios/...)"
    )
    p.add_argument(
        "--baseline", type=Path, default=BASELINE_PADRAO, help="baseline.json para comparar"
    )
    p.add_argument(
        "--tolerancia",
        type=float,
        default=None,
        help="pontos de regressão tolerados (default: do baseline, 2.0)",
    )
    p.add_argument("--limiar-intencao", type=float, default=LIMIARES_PADRAO["intencao"])
    p.add_argument("--limiar-categoria", type=float, default=LIMIARES_PADRAO["categoria"])
    p.add_argument("--limiar-prioridade", type=float, default=LIMIARES_PADRAO["prioridade"])
    p.add_argument("--max", type=int, default=0, help="avalia só os N primeiros casos (depuração)")
    p.add_argument(
        "--atualizar-baseline",
        action="store_true",
        help="grava as métricas desta rodada no baseline (exige --justificativa)",
    )
    p.add_argument("--justificativa", default=None, help="por que o baseline muda (vai para o PR)")
    return p


def carregar_baseline(caminho: Path) -> dict[str, Any]:
    if not caminho.exists():
        return {"versao": 1, "tolerancia_pontos": 2.0, "metricas": {}}
    dado = json.loads(caminho.read_text(encoding="utf-8"))
    if not isinstance(dado, dict) or not isinstance(dado.get("metricas"), dict):
        raise ValueError(f"{caminho}: baseline deve ser objeto com 'metricas'")
    return dado


def main(argv: Sequence[str] | None = None, saida_texto: Callable[[str], None] = print) -> int:
    args = _parser().parse_args(argv)
    if args.atualizar_baseline and not args.justificativa:
        saida_texto("erro: --atualizar-baseline exige --justificativa")
        return 2

    taxonomia = carregar_taxonomia(CAMINHO_TAXONOMIA)
    caminho_dataset = FONTES_ARQUIVO[args.fonte]
    if not caminho_dataset.exists():
        saida_texto(
            f"erro: dataset não existe: {caminho_dataset} (historico vem de evals.anonimizar + rotulagem, P9)"
        )
        return 2
    try:
        casos = carregar_dataset(caminho_dataset, taxonomia)
    except ErroFormato as e:
        saida_texto(f"erro: dataset inválido — {e.campo}: {e.motivo}")
        return 2
    if args.max > 0:
        casos = casos[: args.max]

    classificador, nome_modo = obter_classificador(args.modo)
    avisos: list[str] = []
    if nome_modo != args.modo:
        avisos.append(
            f"modo '{args.modo}' ainda não implementado (TODO(E2.2)); rodando com o classificador '{nome_modo}'"
        )
    if taxonomia.placeholder:
        avisos.append(
            "taxonomia é placeholder (P1): métricas de categoria são informativas, não bloqueiam"
        )

    resultados = rodar(casos, classificador)
    limiares = {
        "intencao": args.limiar_intencao,
        "categoria": args.limiar_categoria,
        "prioridade": args.limiar_prioridade,
    }
    chave = f"{args.fonte}/{args.modo}"
    relatorio = gerar_relatorio(
        resultados,
        limiares,
        titulo=f"Evals de classificação — {chave}",
        avisos=avisos,
        contexto={
            "gerado_em": datetime.now(UTC).isoformat(timespec="seconds"),
            "dataset": f"{caminho_dataset.name} ({len(casos)} casos)",
            "classificador": nome_modo,
            "taxonomia": f"versão {taxonomia.versao}{' (placeholder)' if taxonomia.placeholder else ''}",
            "casos por categoria": ", ".join(
                f"{k}={v}" for k, v in contar_por_categoria(casos).items()
            ),
        },
    )

    caminho_md: Path = args.relatorio or (RELATORIOS / f"classificacao-{args.fonte}-{args.modo}.md")
    caminho_md.parent.mkdir(parents=True, exist_ok=True)
    caminho_md.write_text(relatorio.para_markdown(), encoding="utf-8")
    metricas = relatorio.metricas_planas()
    caminho_json = caminho_md.with_suffix(".json")
    caminho_json.write_text(
        json.dumps(
            {"chave": chave, "limiares": limiares, "metricas": metricas},
            ensure_ascii=False,
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )

    saida_texto(f"relatório: {caminho_md}")
    saida_texto(f"métricas:  {caminho_json}")
    for aviso in avisos:
        saida_texto(f"aviso: {aviso}")
    for p in relatorio.perguntas:
        f1 = f"{p.macro_f1 * 100:.1f}%" if p.macro_f1 is not None else "—"
        acc_lim = f"{p.acuracia_no_limiar * 100:.1f}%" if p.acuracia_no_limiar is not None else "—"
        saida_texto(
            f"{p.pergunta:<11} n={p.n:<4} acurácia={p.acuracia * 100:5.1f}%  macro-F1={f1:<6}  "
            f"cobertura@{p.limiar:.2f}={p.cobertura_no_limiar * 100:5.1f}%  acurácia@limiar={acc_lim:<6}  ECE={p.ece:.3f}"
        )
    saida_texto(
        f"latência    p50={relatorio.latencia_p50_ms:.1f} ms  p95={relatorio.latencia_p95_ms:.1f} ms"
    )

    try:
        baseline = carregar_baseline(args.baseline)
    except ValueError as e:
        saida_texto(f"erro: {e}")
        return 2
    tolerancia = (
        args.tolerancia
        if args.tolerancia is not None
        else float(baseline.get("tolerancia_pontos", 2.0))
    )
    base_chave = baseline["metricas"].get(chave, {})
    regressoes = (
        comparar_com_baseline(metricas, base_chave, tolerancia)
        if isinstance(base_chave, dict)
        else []
    )

    if args.atualizar_baseline:
        baseline["metricas"][chave] = metricas
        baseline["atualizado_em"] = datetime.now(UTC).date().isoformat()
        baseline["justificativa"] = args.justificativa
        args.baseline.write_text(
            json.dumps(baseline, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
        saida_texto(f"baseline atualizado: {args.baseline} [{chave}]")
        return 0

    if regressoes:
        saida_texto(f"REGRESSÃO contra o baseline ({chave}, tolerância {tolerancia} pts):")
        for r in regressoes:
            saida_texto(f"  - {r}")
        return 1
    if base_chave:
        saida_texto(f"sem regressão contra o baseline ({chave}, tolerância {tolerancia} pts)")
    else:
        saida_texto(f"baseline sem métricas para {chave}: nada a comparar")
    return 0


if __name__ == "__main__":
    _reconfigurar = getattr(sys.stdout, "reconfigure", None)
    if _reconfigurar is not None:
        _reconfigurar(encoding="utf-8")
    sys.exit(main())
