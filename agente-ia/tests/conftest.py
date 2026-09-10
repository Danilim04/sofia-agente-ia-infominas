"""Configuração compartilhada de testes.

Carrega o `.env` real (se existir) para que testes de integração possam
exercitar serviços externos com chaves reais. Em ambientes sem `.env`
(CI, dev fresco), usa env vars dummy para `src.config.get_settings()`
ainda validar — esses casos resultam em testes de integração pulados via
`pytest.mark.skipif`.

Também isola o estado mutável dos mocks de CRM entre testes — sem isso,
um teste que chama `abrir_novo_chamado` deixaria tickets pendurados no
dicionário global e quebraria asserções de outros testes ("CLI-1001 sem
chamados").
"""

from __future__ import annotations

import copy
import os

import pytest
from dotenv import load_dotenv

load_dotenv(override=False)

os.environ.setdefault("OPENROUTER_API_KEY", "test-openrouter-key")


@pytest.fixture(autouse=True)
def _isolar_estado_crm_mocks():
    """Snapshot/restore do `_CHAMADOS` mutável entre testes."""
    from src.tools import crm_mocks

    snapshot = copy.deepcopy(crm_mocks._CHAMADOS)
    try:
        yield
    finally:
        crm_mocks._CHAMADOS.clear()
        crm_mocks._CHAMADOS.update(snapshot)
