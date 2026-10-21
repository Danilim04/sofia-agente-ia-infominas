"""Configuração da identificação no cérebro (espelha o gateway Go)."""

from __future__ import annotations

import os

_VERDADEIROS = {"1", "true", "t", "yes", "y", "sim", "on"}


def ident_exigir_cpf() -> bool:
    """`IDENT_EXIGIR_CPF` (default **false**): telefone reconhecido pelo lookup
    do Infodesk dispensa CPF/contrato (`docs/infodesk/07` N3, hipótese de plano;
    ADR 0004, revisão 2026-10-20). Com `true` o lookup não basta e a tool
    `informar_identificacao` volta a ser obrigatória.

    TODO(N3): a Infominas decide se telefone conhecido basta para abrir chamado.
    """
    return os.environ.get("IDENT_EXIGIR_CPF", "").strip().lower() in _VERDADEIROS
