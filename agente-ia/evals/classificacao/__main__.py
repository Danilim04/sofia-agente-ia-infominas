# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""CLI: `python -m evals.classificacao --fonte sintetico|historico [--jev|--llm|--offline|--nulo]`.

Modos (todos usam o `src.classificacao.classificar()` real, exceto `--nulo`):

- `--offline` (padrão): JEV/fallback SIMULADOS; sem rede nem chave; roda no CI. Os
  números medem o encanamento, **não** o JEV — o relatório sai com essa faixa no topo;
- `--jev`: JEV real (`TYPESAFE_API_KEY`, P8); usa o fallback LLM real se houver
  `OPENROUTER_API_KEY`; testes `-m jev`;
- `--llm`: só o fallback LLM real (`OPENROUTER_API_KEY`); testes `-m llm_real`;
- `--nulo`: classificador trivial (smoke do encanamento).

`--janelas [1,3,6,10]` repete a rodada com cada janela de contexto (`JEV_JANELA_MSGS`,
C-JEV1) e acrescenta a tabela comparativa; `--janela N` fixa a janela do detalhe.

Saídas: relatório Markdown (default `evals/relatorios/<data>-classificacao-<fonte>-<modo>.md`)
e um `.json` ao lado com as métricas planas (0–100) para comparar com
`evals/baseline.json`. Código de saída: 0 ok; 1 regressão acima da tolerância
contra o baseline (quando `--baseline` tem números para `<fonte>/<modo>`);
2 dataset inválido, modo sem chave/SDK ou argumento errado.
"""

from __future__ import annotations

import argparse
import json
import sys
from collections.abc import Callable, Sequence
from dataclasses import replace
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from evals.classificacao import (
    FONTES_ARQUIVO,
    JANELAS_PADRAO,
    LIMIARES_PADRAO,
    carregar_dataset,
    classificador_nulo,
    contar_por_categoria,
    rodar,
)
from evals.classificacao.adaptador import DescricaoModo, ErroModo, criar_classificador
from evals.formatos import CAMINHO_TAXONOMIA, CasoClassificacao, ErroFormato, carregar_taxonomia
from evals.relatorio import (
    MetricasPergunta,
    Relatorio,
    ResultadoCaso,
    comparar_com_baseline,
    comparar_janelas,
    gerar_relatorio,
)

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
        help="JEV real (exige TYPESAFE_API_KEY, P8; testes -m jev)",
    )
    modo.add_argument(
        "--llm",
        action="store_const",
        const="llm",
        dest="modo",
        help="só o fallback LLM real (exige OPENROUTER_API_KEY; testes -m llm_real)",
    )
    modo.add_argument(
        "--offline",
        action="store_const",
        const="offline",
        dest="modo",
        help="JEV/LLM simulados (padrão): sem rede, roda no CI; NÃO mede o JEV",
    )
    modo.add_argument(
        "--nulo",
        action="store_const",
        const="nulo",
        dest="modo",
        help="classificador nulo explícito (smoke do encanamento)",
    )
    p.set_defaults(modo="offline")
    p.add_argument(
        "--janela",
        type=int,
        default=None,
        help="mensagens enviadas ao JEV (JEV_JANELA_MSGS); com --janelas, a do detalhe",
    )
    p.add_argument(
        "--janelas",
        nargs="?",
        const=",".join(str(j) for j in JANELAS_PADRAO),
        default=None,
        metavar="1,3,6,10",
        help="compara janelas de contexto (C-JEV1); sem valor, usa 1,3,6,10",
    )
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


def _ece(valor: float | None) -> str:
    return "—" if valor is None else f"{valor:.3f}"


def carregar_baseline(caminho: Path) -> dict[str, Any]:
    if not caminho.exists():
        return {"versao": 1, "tolerancia_pontos": 2.0, "metricas": {}}
    dado = json.loads(caminho.read_text(encoding="utf-8"))
    if not isinstance(dado, dict) or not isinstance(dado.get("metricas"), dict):
        raise ValueError(f"{caminho}: baseline deve ser objeto com 'metricas'")
    return dado


BANNER_OFFLINE = (
    "Exemplo offline com fakes, não é medição real. Os números medem o encanamento do runner "
    "(roteador + JEV/fallback simulados), nunca o desempenho do JEV (P8)."
)
BANNER_NULO = "Classificador nulo: só prova o encanamento do runner. Não é medição."
#: Janela de contexto de referência (docs/plano/02, C-JEV1): o baseline sem sufixo é a dela.
JANELA_BASELINE = 6


def _inteiros(texto: str) -> list[int]:
    valores = [int(parte) for parte in texto.split(",") if parte.strip()]
    if not valores or any(v < 1 for v in valores):
        raise ValueError(f"janelas inválidas: {texto!r} (inteiros ≥ 1 separados por vírgula)")
    return sorted(set(valores))


def _rodar_janela(
    modo: str, janela: int | None, casos: Sequence[CasoClassificacao]
) -> tuple[list[ResultadoCaso], DescricaoModo | None]:
    """Uma rodada completa do dataset com o classificador do `modo` (e `janela` de contexto)."""
    if modo == "nulo":
        return rodar(casos, classificador_nulo), None
    classificador, descricao = criar_classificador(modo, janela)
    try:
        return rodar(casos, classificador), descricao
    finally:
        classificador.fechar()


def _linha_resumo(p: MetricasPergunta) -> str:
    f1 = f"{p.macro_f1 * 100:.1f}%" if p.macro_f1 is not None else "—"
    acc_lim = f"{p.acuracia_no_limiar * 100:.1f}%" if p.acuracia_no_limiar is not None else "—"
    return (
        f"{p.pergunta:<15} n={p.n:<4} acurácia={p.acuracia * 100:5.1f}%  macro-F1={f1:<6}  "
        f"cobertura@{p.limiar:.2f}={p.cobertura_no_limiar * 100:5.1f}%  "
        f"acurácia@limiar={acc_lim:<6}  ECE={_ece(p.ece)}"
    )


def main(argv: Sequence[str] | None = None, saida_texto: Callable[[str], None] = print) -> int:
    args = _parser().parse_args(argv)
    if args.atualizar_baseline and not args.justificativa:
        saida_texto("erro: --atualizar-baseline exige --justificativa")
        return 2
    if args.atualizar_baseline and args.modo == "offline":
        saida_texto(
            "erro: --atualizar-baseline recusado — o modo offline usa fakes e não é referência "
            "(use --jev ou --llm)"
        )
        return 2
    try:
        janelas = _inteiros(args.janelas) if args.janelas else []
    except ValueError as e:
        saida_texto(f"erro: {e}")
        return 2
    if args.janela is not None and args.janela < 1:
        saida_texto("erro: --janela deve ser ≥ 1")
        return 2
    if args.modo == "nulo" and (janelas or args.janela):
        saida_texto("erro: janelas não se aplicam ao classificador nulo (ele não lê o estado)")
        return 2

    taxonomia = carregar_taxonomia(CAMINHO_TAXONOMIA)
    caminho_dataset = FONTES_ARQUIVO[args.fonte]
    if not caminho_dataset.exists():
        saida_texto(
            f"erro: dataset não existe: {caminho_dataset} "
            "(historico vem de evals.anonimizar + rotulagem, P9)"
        )
        return 2
    try:
        casos = carregar_dataset(caminho_dataset, taxonomia)
    except ErroFormato as e:
        saida_texto(f"erro: dataset inválido — {e.campo}: {e.motivo}")
        return 2
    if args.max > 0:
        casos = casos[: args.max]

    # Janela do detalhe: a pedida; com --janelas e sem --janela, 6 (hipótese C-JEV1) se estiver na lista.
    detalhe: int | None = args.janela
    if janelas and detalhe is None:
        detalhe = JANELA_BASELINE if JANELA_BASELINE in janelas else janelas[0]
    if janelas and detalhe is not None and detalhe not in janelas:
        janelas = sorted({*janelas, detalhe})
    a_rodar: list[int | None] = list(janelas) if janelas else [detalhe]

    limiares = {
        **LIMIARES_PADRAO,
        "intencao": args.limiar_intencao,
        "categoria": args.limiar_categoria,
        "categoria_final": args.limiar_categoria,
        "prioridade": args.limiar_prioridade,
        "prioridade_final": args.limiar_prioridade,
    }
    avisos: list[str] = []
    if taxonomia.placeholder:
        avisos.append(
            "taxonomia é placeholder (P1): métricas de categoria são informativas, não bloqueiam"
        )

    resultados_por_janela: dict[int | None, list[ResultadoCaso]] = {}
    descricoes: dict[int | None, DescricaoModo | None] = {}
    relatorios_por_janela: dict[int, Relatorio] = {}
    for janela in a_rodar:
        try:
            resultados, descricao = _rodar_janela(args.modo, janela, casos)
        except ErroModo as e:
            saida_texto(f"erro: {e}")
            return 2
        resultados_por_janela[janela] = resultados
        descricoes[janela] = descricao
        if janela is not None:
            relatorios_por_janela[janela] = gerar_relatorio(resultados, limiares)

    descricao_final = descricoes[detalhe]
    if descricao_final is not None:
        avisos.extend(descricao_final.avisos)
    sem_jev = sum(1 for r in resultados_por_janela[detalhe] if r.extras.get("jev_indisponivel"))
    if sem_jev:
        avisos.append(
            f"o JEV não respondeu em {sem_jev} de {len(resultados_por_janela[detalhe])} casos: "
            "nesses turnos intencao/categoria valem a resposta do fallback com confiança 0, "
            "o que rebaixa a acurácia e distorce a calibração do JEV (veja a taxa em Pipeline)"
        )
    chave = f"{args.fonte}/{args.modo}"
    if descricao_final is not None and descricao_final.janela != JANELA_BASELINE:
        # janela efetiva fora da referência (flag, `--janelas` sem o 6 ou JEV_JANELA_MSGS do
        # ambiente) não se compara nem se grava como o baseline "padrão"
        chave = f"{chave}@j{descricao_final.janela}"

    banner = (
        BANNER_OFFLINE if args.modo == "offline" else BANNER_NULO if args.modo == "nulo" else ""
    )
    contexto = {
        "gerado_em": datetime.now(UTC).isoformat(timespec="seconds"),
        "dataset": f"{caminho_dataset.name} ({len(casos)} casos)",
        "modo": args.modo,
        "JEV": descricao_final.jev if descricao_final else "nenhum (classificador nulo)",
        "fallback LLM": descricao_final.fallback if descricao_final else "nenhum",
        "janela de contexto": (
            f"{descricao_final.janela} mensagens (JEV_JANELA_MSGS)" if descricao_final else "—"
        ),
        "latência": (
            "simulada (determinística), não é a do JEV"
            if descricao_final and descricao_final.simulado
            else "medida no cliente (inclui o fallback quando chamado)"
        ),
        "taxonomia": f"versão {taxonomia.versao}{' (placeholder)' if taxonomia.placeholder else ''}",
        "casos por categoria": ", ".join(
            f"{k}={v}" for k, v in contar_por_categoria(casos).items()
        ),
    }
    relatorio = replace(
        gerar_relatorio(
            resultados_por_janela[detalhe],
            limiares,
            titulo=f"Evals de classificação — {chave}",
            avisos=avisos,
            contexto=contexto,
        ),
        banner=banner,
        secoes_extra=(comparar_janelas(relatorios_por_janela),)
        if len(relatorios_por_janela) > 1
        else (),
    )

    data = datetime.now(UTC).date().isoformat()
    sufixo = "-janelas" if len(relatorios_por_janela) > 1 else ""
    caminho_md: Path = args.relatorio or (
        RELATORIOS / f"{data}-classificacao-{args.fonte}-{args.modo}{sufixo}.md"
    )
    caminho_md.parent.mkdir(parents=True, exist_ok=True)
    caminho_md.write_text(relatorio.para_markdown(), encoding="utf-8", newline="\n")
    metricas = relatorio.metricas_planas()
    dado_json: dict[str, Any] = {
        "chave": chave,
        "modo": args.modo,
        "simulado": args.modo in {"offline", "nulo"},
        "banner": banner,
        "limiares": limiares,
        "metricas": metricas,
    }
    if len(relatorios_por_janela) > 1:
        dado_json["janelas"] = {
            str(j): r.metricas_planas() for j, r in sorted(relatorios_por_janela.items())
        }
    caminho_json = caminho_md.with_suffix(".json")
    caminho_json.write_text(
        json.dumps(dado_json, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )

    saida_texto(f"relatório: {caminho_md}")
    saida_texto(f"métricas:  {caminho_json}")
    if banner:
        saida_texto(f"aviso: {banner}")
    for aviso in avisos:
        saida_texto(f"aviso: {aviso}")
    for p in relatorio.perguntas:
        saida_texto(_linha_resumo(p))
    saida_texto(
        f"latência    p50={relatorio.latencia_p50_ms:.1f} ms  p95={relatorio.latencia_p95_ms:.1f} ms"
    )
    for janela, r in (
        sorted(relatorios_por_janela.items()) if len(relatorios_por_janela) > 1 else ()
    ):
        cat = next((p for p in r.perguntas if p.pergunta == "categoria"), None)
        f1 = f"{cat.macro_f1 * 100:.1f}%" if cat and cat.macro_f1 is not None else "—"
        saida_texto(f"janela {janela:>2}: categoria macro-F1={f1}")

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
        for regressao in regressoes:
            saida_texto(f"  - {regressao}")
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
