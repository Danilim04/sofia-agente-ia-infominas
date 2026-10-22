# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""`python -m evals.conversa [--sem-executar] [--relatorio ARQ.md]`: valida e executa os cenários.

Falha (exit 1) quando um cenário SEM `xfail` falha, ou quando um com `xfail`
passa (marcador velho). Para rodar como teste bloqueante de PR:
`pytest -m evals_pr evals/`.
"""

from __future__ import annotations

import logging
import sys
from pathlib import Path

from evals.conversa.runner import (
    ResultadoCenario,
    carregar_cenarios,
    executar,
    relatorio_markdown,
    resumo,
)
from evals.formatos import ErroFormato


def main(argv: list[str] | None = None) -> int:
    reconfigurar = getattr(sys.stdout, "reconfigure", None)
    if reconfigurar is not None:
        reconfigurar(encoding="utf-8")
    # O grafo loga avisos de política/guardrail a cada cenário; o relatório é só a tabela.
    logging.disable(logging.CRITICAL)
    args = argv if argv is not None else sys.argv[1:]
    sem_executar = "--sem-executar" in args
    destino: Path | None = None
    if "--relatorio" in args:
        i = args.index("--relatorio")
        if i + 1 >= len(args):
            print("erro: --relatorio exige o caminho do arquivo .md")
            return 2
        destino = Path(args[i + 1])
    try:
        cenarios = carregar_cenarios()
    except (ErroFormato, ValueError) as e:
        print(f"erro: {e}")
        return 2
    resultados: dict[str, ResultadoCenario] = {}
    if not sem_executar:
        for c in cenarios:
            resultados[c.id] = executar(c)
    print(resumo(cenarios, resultados))
    if destino is not None:
        destino.parent.mkdir(parents=True, exist_ok=True)
        destino.write_text(relatorio_markdown(cenarios, resultados), encoding="utf-8")
        print(f"relatório: {destino}")
    if sem_executar:
        return 0
    ruins = 0
    for c in cenarios:
        r = resultados[c.id]
        if r.passou and c.xfail:
            print(f"\n{c.id}: PASSOU mas está marcado xfail ({c.xfail}) — remova o marcador")
            ruins += 1
        elif not r.passou:
            titulo = "xfail esperado" if c.xfail else "FALHOU"
            print(f"\n{c.id}: {titulo}" + (f" — {c.xfail}" if c.xfail else ""))
            for falha in r.falhas:
                print(f"  - {falha}")
            ruins += 0 if c.xfail else 1
    xf = sum(1 for c in cenarios if c.xfail)
    print(f"\n{len(cenarios) - xf} cenário(s) normais, {xf} xfail; {ruins} problema(s)")
    return 1 if ruins else 0


if __name__ == "__main__":
    sys.exit(main())
