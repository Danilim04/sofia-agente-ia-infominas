"""Número de contrato do cliente: validação configurável e máscara.

O formato do número de contrato da Infominas não está documentado
(TODO(N3): confirmar com a Infominas). Hipótese de trabalho, configurável por
`IDENT_CONTRATO_RE` (mesma variável do gateway): 4 a 12 dígitos.
"""

from __future__ import annotations

import logging
import re
from typing import Any

from src.identity.config import CONTRATO_RE_PADRAO, carregar

logger = logging.getLogger(__name__)

__all__ = [
    "CONTRATO_RE_PADRAO",
    "contrato_valido",
    "mascarar_contrato",
    "normalizar_contrato",
]


def _padrao() -> re.Pattern[str]:
    bruto = carregar().ident_contrato_re.strip() or CONTRATO_RE_PADRAO
    try:
        return re.compile(bruto)
    except re.error:
        logger.warning("IDENT_CONTRATO_RE inválida; usando o default %s", CONTRATO_RE_PADRAO)
        return re.compile(CONTRATO_RE_PADRAO)


def normalizar_contrato(valor: Any) -> str:
    """Remove espaços nas pontas; o resto o cliente digitou e a regex decide."""
    return "" if valor is None else str(valor).strip()


def contrato_valido(valor: Any) -> bool:
    """`valor` casa com `IDENT_CONTRATO_RE` (default 4 a 12 dígitos)."""
    texto = normalizar_contrato(valor)
    return bool(texto) and _padrao().fullmatch(texto) is not None


def mascarar_contrato(valor: Any) -> str:
    """`******456` — só os 3 últimos caracteres ficam visíveis."""
    texto = normalizar_contrato(valor)
    if len(texto) <= 3:
        return "*" * max(len(texto), 3)
    return "*" * (len(texto) - 3) + texto[-3:]
