# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""`python -m evals.conversa`: lista e valida os cenários (execução em E3.5).

Para rodar como teste bloqueante de PR: `pytest -m evals_pr evals/`.
"""

from __future__ import annotations

import sys

from evals.conversa.runner import carregar_cenarios, resumo
from evals.formatos import ErroFormato


def main() -> int:
    reconfigurar = getattr(sys.stdout, "reconfigure", None)
    if reconfigurar is not None:
        reconfigurar(encoding="utf-8")
    try:
        cenarios = carregar_cenarios()
    except (ErroFormato, ValueError) as e:
        print(f"erro: {e}")
        return 2
    print(resumo(cenarios))
    return 0


if __name__ == "__main__":
    sys.exit(main())
