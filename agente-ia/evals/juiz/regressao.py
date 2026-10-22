# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Regressão dos evals noturnos: compara os relatórios `.json` com os baselines guardados.

Quem usa: o workflow `evals-noturnos.yml`, depois de rodar a classificação (`--jev`) e o
juiz. Duas famílias, mesma tolerância (default 2 pts, a do plano em `docs/plano/04`):

- classificação (`evals/baseline.json`, chave `<fonte>/<modo>` do relatório): queda em
  `*.macro_f1` ou `*.acuracia_no_limiar`, ou aumento de `*.ece`, acima da tolerância;
- juiz (`evals/juiz/baseline.json`, chave `juiz/noturno`): queda em qualquer `juiz.*`
  (todas são 0–100, maior é melhor) acima da tolerância.

Só compara métricas que existem nos DOIS lados: baseline vazio (hoje, enquanto não há
chave do JEV, P8) nunca acusa regressão. Gera o texto do comentário para o último PR
mergeado e escreve `regressao=true|false` no `$GITHUB_OUTPUT`.

    python -m evals.juiz.regressao --classificacao evals/relatorios/x.json --juiz evals/relatorios/juiz.json \
        --saida evals/relatorios/comentario.md
"""

from __future__ import annotations

import argparse
import json
import os
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Final

from evals.relatorio import METRICAS_BLOQUEANTES, METRICAS_MENOR_MELHOR

RAIZ_EVALS: Final[Path] = Path(__file__).resolve().parent.parent
BASELINE_CLASSIFICACAO: Final[Path] = RAIZ_EVALS / "baseline.json"
BASELINE_JUIZ: Final[Path] = Path(__file__).resolve().parent / "baseline.json"
TOLERANCIA_PADRAO: Final[float] = 2.0
MARCADOR: Final[str] = "<!-- evals-noturnos -->"


@dataclass(frozen=True)
class Regressao:
    familia: str
    chave: str
    metrica: str
    base: float
    atual: float
    #: pontos perdidos (positivo = piorou); para ECE, o aumento
    piora: float

    def linha(self) -> str:
        return (
            f"| {self.familia} | `{self.chave}` | `{self.metrica}` | {self.base:.2f} | "
            f"{self.atual:.2f} | {self.piora:+.2f} |"
        )


def comparar(
    familia: str,
    chave: str,
    atuais: Mapping[str, float],
    baseline: Mapping[str, float],
    tolerancia: float,
    sentido: Callable[[str], int],
) -> list[Regressao]:
    """Regressões acima da tolerância. `sentido(metrica)`: +1 maior é melhor, −1 menor é melhor, 0 ignora."""
    achadas: list[Regressao] = []
    for metrica, base in sorted(baseline.items()):
        s = sentido(metrica)
        atual = atuais.get(metrica)
        if s == 0 or atual is None:
            continue
        piora = (base - atual) if s > 0 else (atual - base)
        if piora > tolerancia:
            achadas.append(Regressao(familia, chave, metrica, float(base), float(atual), piora))
    return achadas


def _sentido_classificacao(metrica: str) -> int:
    if any(metrica.endswith("." + m) for m in METRICAS_BLOQUEANTES):
        return 1
    if any(metrica.endswith("." + m) for m in METRICAS_MENOR_MELHOR):
        return -1
    return 0


def _sentido_juiz(metrica: str) -> int:
    return 1 if metrica.startswith("juiz.") else 0


def regressoes_classificacao(
    relatorio: Mapping[str, Any], baseline: Mapping[str, Any], tolerancia: float | None = None
) -> list[Regressao]:
    chave = str(relatorio.get("chave", ""))
    base = (baseline.get("metricas") or {}).get(chave) or {}
    tol = (
        tolerancia
        if tolerancia is not None
        else float(baseline.get("tolerancia_pontos", TOLERANCIA_PADRAO))
    )
    return comparar(
        "classificação", chave, relatorio.get("metricas") or {}, base, tol, _sentido_classificacao
    )


def regressoes_juiz(
    relatorio: Mapping[str, Any], baseline: Mapping[str, Any], tolerancia: float | None = None
) -> list[Regressao]:
    chave = str(relatorio.get("chave", "juiz/noturno"))
    base = (baseline.get("metricas") or {}).get(chave) or {}
    tol = (
        tolerancia
        if tolerancia is not None
        else float(baseline.get("tolerancia_pontos", TOLERANCIA_PADRAO))
    )
    return comparar("juiz", chave, relatorio.get("metricas") or {}, base, tol, _sentido_juiz)


def montar_comentario(regressoes: Sequence[Regressao], link_execucao: str = "") -> str:
    """Texto do comentário no último PR mergeado (o marcador deixa achar/atualizar depois)."""
    linhas = [
        MARCADOR,
        "## Evals noturnos: regressão detectada",
        "",
        "A rodada noturna da `main` ficou pior que o baseline guardado em mais que a tolerância "
        f"({TOLERANCIA_PADRAO:g} pts). Este é o último PR mergeado; pode não ser o culpado "
        "(o juiz é um LLM e oscila), mas vale conferir.",
        "",
        "| família | relatório | métrica | baseline | agora | piora (pts) |",
        "|---|---|---|---:|---:|---:|",
        *[r.linha() for r in regressoes],
        "",
    ]
    if link_execucao:
        linhas.append(f"Relatório completo (artefato `relatorios-evals`): {link_execucao}")
    linhas.append(
        "Não bloqueia merge (camada 2, docs/plano/04). Se a mudança é intencional, regrave o baseline no PR."
    )
    return "\n".join(linhas) + "\n"


def _ler_json(caminho: Path) -> dict[str, Any]:
    dado = json.loads(caminho.read_text(encoding="utf-8"))
    if not isinstance(dado, dict):
        raise ValueError(f"{caminho}: esperado um objeto JSON")
    return dado


def _link_execucao() -> str:
    servidor, repo, run = (
        os.environ.get(v, "") for v in ("GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_RUN_ID")
    )
    return f"{servidor}/{repo}/actions/runs/{run}" if servidor and repo and run else ""


def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="python -m evals.juiz.regressao", description=__doc__)
    p.add_argument(
        "--classificacao",
        type=Path,
        action="append",
        default=[],
        help="relatório .json (repetível)",
    )
    p.add_argument("--juiz", type=Path, default=None, help="relatório .json do juiz")
    p.add_argument("--baseline-classificacao", type=Path, default=BASELINE_CLASSIFICACAO)
    p.add_argument("--baseline-juiz", type=Path, default=BASELINE_JUIZ)
    p.add_argument(
        "--tolerancia", type=float, default=None, help="pontos (default: do baseline, 2.0)"
    )
    p.add_argument(
        "--saida", type=Path, default=None, help="escreve o comentário aqui, se houver regressão"
    )
    p.add_argument("--github-output", type=Path, default=None, help="default: $GITHUB_OUTPUT")
    return p


def main(argv: Sequence[str] | None = None, saida_texto: Callable[[str], None] = print) -> int:
    """0 = comparou (com ou sem regressão: ela vai em `regressao=` e no comentário); 2 = entrada ilegível."""
    args = _parser().parse_args(argv)
    achadas: list[Regressao] = []
    try:
        for caminho in args.classificacao:
            if not caminho.exists():
                saida_texto(f"aviso: {caminho} não existe (rodada não gerou o relatório)")
                continue
            base = (
                _ler_json(args.baseline_classificacao)
                if args.baseline_classificacao.exists()
                else {}
            )
            achadas += regressoes_classificacao(_ler_json(caminho), base, args.tolerancia)
        if args.juiz is not None:
            if not args.juiz.exists():
                saida_texto(f"aviso: {args.juiz} não existe (rodada não gerou o relatório)")
            else:
                base = _ler_json(args.baseline_juiz) if args.baseline_juiz.exists() else {}
                achadas += regressoes_juiz(_ler_json(args.juiz), base, args.tolerancia)
    except (ValueError, OSError) as e:
        saida_texto(f"erro: {e}")
        return 2
    if achadas:
        saida_texto(f"REGRESSÃO em {len(achadas)} métrica(s):")
        for r in achadas:
            saida_texto(
                f"  - {r.familia} {r.chave} {r.metrica}: {r.base:.2f} → {r.atual:.2f} ({r.piora:+.2f} pts)"
            )
        if args.saida is not None:
            args.saida.parent.mkdir(parents=True, exist_ok=True)
            args.saida.write_text(
                montar_comentario(achadas, _link_execucao()), encoding="utf-8", newline="\n"
            )
    else:
        saida_texto("sem regressão contra os baselines (ou baseline sem métricas para comparar)")
    destino = args.github_output or (
        Path(os.environ["GITHUB_OUTPUT"]) if os.environ.get("GITHUB_OUTPUT") else None
    )
    if destino is not None:
        with destino.open("a", encoding="utf-8", newline="\n") as f:
            f.write(f"regressao={'true' if achadas else 'false'}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
