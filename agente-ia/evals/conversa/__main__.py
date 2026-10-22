# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""`python -m evals.conversa [--sem-executar]`: valida e executa os cenários, com relatório.

Falha (exit 1) quando um cenário SEM `xfail` falha, ou quando um com `xfail`
passa (marcador velho). Para rodar como teste bloqueante de PR:
`pytest -m evals_pr evals/`.
"""

from __future__ import annotations

import sys

from evals.conversa.runner import ResultadoCenario, carregar_cenarios, executar, resumo
from evals.formatos import ErroFormato


def main(argv: list[str] | None = None) -> int:
    reconfigurar = getattr(sys.stdout, "reconfigure", None)
    if reconfigurar is not None:
        reconfigurar(encoding="utf-8")
    sem_executar = "--sem-executar" in (argv if argv is not None else sys.argv[1:])
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
