"""Configuração compartilhada de testes.

Carrega o `.env` real (se existir) para que testes de integração possam
exercitar serviços externos com chaves reais. Em ambientes sem `.env`
(CI, dev fresco), usa env vars dummy para `src.config.get_settings()`
ainda validar — esses casos resultam em testes de integração pulados.

Também zera o registro de execuções das tools de teste (`tests/fakes.py`)
entre testes, para uma abertura contada num teste não vazar para outro.
"""

from __future__ import annotations

import os

import pytest
from dotenv import load_dotenv

load_dotenv(override=False)

os.environ.setdefault("OPENROUTER_API_KEY", "test-openrouter-key")


@pytest.fixture(autouse=True)
def _zerar_registro_das_tools_de_teste():
    from tests import fakes

    fakes.CHAMADAS.clear()
    try:
        yield
    finally:
        fakes.CHAMADAS.clear()
