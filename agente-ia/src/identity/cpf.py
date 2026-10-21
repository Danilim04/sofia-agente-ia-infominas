"""CPF: normalização, dígitos verificadores e máscara.

O CPF é PII (regra 5 do CLAUDE.md): em claro só existe em memória, durante o
turno em que o cliente o digitou. O que vai a log, estado e disco é a forma
mascarada (`***.***.789-**`). A validação aqui é a mesma do gateway
(`backend/internal/toolsapi/util.go`) — o Go revalida server-side.
"""

from __future__ import annotations

import re
from typing import Any


def somente_digitos(valor: Any) -> str:
    """Só os dígitos ASCII de `valor` ("123.456.789-09" → "12345678909")."""
    if valor is None:
        return ""
    return "".join(c for c in str(valor) if c in "0123456789")


def _digito(base: str, peso_inicial: int) -> int:
    soma = sum(int(c) * (peso_inicial - i) for i, c in enumerate(base))
    resto = (soma * 10) % 11
    return 0 if resto == 10 else resto


def cpf_valido(valor: Any) -> bool:
    """11 dígitos, não todos iguais, e os dois dígitos verificadores corretos.

    Aceita pontuação (`123.456.789-09`); rejeita qualquer outro lixo (letras
    junto dos números não "viram" um CPF por descartar o resto).
    """
    texto = "" if valor is None else str(valor).strip()
    if not re.fullmatch(r"[0-9.\-\s]+", texto):
        return False
    digitos = somente_digitos(texto)
    if len(digitos) != 11 or len(set(digitos)) == 1:
        return False
    return int(digitos[9]) == _digito(digitos[:9], 10) and int(digitos[10]) == _digito(
        digitos[:10], 11
    )


def mascarar_cpf(valor: Any) -> str:
    """`***.***.789-**` — só os 3 dígitos do meio do bloco final ficam visíveis.

    Entrada que não é um CPF de 11 dígitos devolve a máscara total
    (`***.***.***-**`): nunca vaza pedaço de dado que não sabemos o que é.
    """
    digitos = somente_digitos(valor)
    if len(digitos) != 11:
        return "***.***.***-**"
    return f"***.***.{digitos[6:9]}-**"
