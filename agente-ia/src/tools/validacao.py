"""Regras puras de entrada das tools — usadas pela tool e pela política.

Sem rede e sem estado: a tool normaliza com estas funções e a política
(`tool_policy.py`) valida com as MESMAS, para que o que passa na validação seja
exatamente o que a tool envia ao gateway.
"""

from __future__ import annotations

import hashlib
import re
from collections.abc import Mapping
from typing import Any

#: Limites do contrato (`docs/plano/01-ferramentas.md`, A2; `erro-campo_invalido`).
TITULO_MAX = 120
DESCRICAO_MAX = 4000

#: Enum que o LLM pode passar em `transferir_para_atendente` (A6). As demais
#: origens (mídia, falha, frustração, confiança) são decisão de código no gateway.
MOTIVOS_TRANSFERENCIA: tuple[str, ...] = ("pedido_cliente", "fora_do_escopo", "nao_resolvido")

_NUMERO = re.compile(r"[0-9]{1,10}")


def normalizar_numero_chamado(valor: Any) -> str | None:
    """Número do chamado como dígitos (1–10) ou `None` se não for um.

    Tolera espaços nas pontas e um `#` inicial ("#48213"); nada além de
    dígitos ASCII — o número vira inteiro na rota do Infodesk, no gateway.
    """
    if valor is None:
        return None
    texto = str(valor).strip().removeprefix("#").strip()
    return texto if _NUMERO.fullmatch(texto) else None


def chave_idempotencia(conversation_id: str, proposta: Mapping[str, Any]) -> str:
    """`sha256(conversation_id + titulo + categoria + criada_em)` em hex (ADR 0005).

    Calculada a partir da proposta CARIMBADA do estado; o gateway recalcula a
    mesma fórmula (`backend/internal/toolsapi/util.go`) e não confia na nossa.
    """
    bruto = (
        f"{conversation_id}{proposta.get('titulo') or ''}"
        f"{proposta.get('categoria') or ''}{proposta.get('criada_em') or ''}"
    )
    return hashlib.sha256(bruto.encode("utf-8")).hexdigest()
