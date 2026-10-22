"""Validação dos dados do cadastro de solicitante (ADR 0009, `docs/plano/01` A8).

Regras puras, sem rede: a política (`tool_policy.py`) valida com as MESMAS que a
tool normaliza, e o gateway Go as repete (`backend/internal/toolsapi/cadastro.go`)
— o LLM só repassa o que o cliente disse; quem decide o que vale é o código.

- nome: duas palavras ou mais, só letras (com acento) e ``'`` ``-`` ``.``;
- e-mail: opcional; ``^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$``, até 254 caracteres;
- empresa: uma linha, 4 a 120 caracteres (letras/dígitos ≥ 4). É TEXTO de busca: quem escolhe
  o cliente (``clientId``) é o gateway, entre os candidatos da busca.
"""

from __future__ import annotations

import re
import unicodedata
from typing import Any

NOME_MAX = 120
EMPRESA_MAX = 120
EMPRESA_MIN = 4  # letras/dígitos; espelha `empresaMin` do gateway
EMAIL_MAX = 254

_EMAIL = re.compile(r"^[^@\s]+@[^@\s]+\.[^@\s]+$")


def linha_unica(valor: Any) -> str:
    """Texto do cliente em UMA linha: controles viram espaço e os espaços se colapsam."""
    if valor is None:
        return ""
    texto = "".join(" " if unicodedata.category(c).startswith("C") else c for c in str(valor))
    return " ".join(texto.split())


def nome_valido(valor: Any) -> bool:
    nome = linha_unica(valor)
    if not nome or len(nome) > NOME_MAX:
        return False
    palavras = nome.split()
    if len(palavras) < 2:
        return False
    for palavra in palavras:
        if not any(c.isalpha() for c in palavra):
            return False
        if any(not (c.isalpha() or c in "'’-.") for c in palavra):
            return False
    return True


def email_valido(valor: Any) -> bool:
    """Vazio é válido (campo opcional)."""
    email = "" if valor is None else str(valor).strip()
    if not email:
        return True
    if len(email) > EMAIL_MAX or not _EMAIL.match(email):
        return False
    return not any(c.isspace() or unicodedata.category(c).startswith("C") for c in email)


def empresa_valida(valor: Any) -> bool:
    """Uma linha, ≥ `EMPRESA_MIN` letras/dígitos (impede listar a carteira digitando "a"), ≤ 120."""
    empresa = linha_unica(valor)
    letras_e_digitos = sum(c.isalnum() for c in empresa)
    return letras_e_digitos >= EMPRESA_MIN and len(empresa) <= EMPRESA_MAX
