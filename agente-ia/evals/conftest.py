"""Configuração do pytest para `pytest evals/` (rodado explicitamente; `testpaths` é `tests`).

Registra o marker `evals_pr` caso o pyproject não seja encontrado (rootdir
diferente) e garante que nenhum teste de evals dependa de rede ou de `.env`.
"""

from __future__ import annotations

import os

import pytest


def pytest_configure(config: pytest.Config) -> None:
    config.addinivalue_line(
        "markers", "evals_pr: evals determinísticos que bloqueiam merge (pytest -m evals_pr evals/)"
    )


@pytest.fixture(autouse=True)
def _sem_chaves_reais(monkeypatch: pytest.MonkeyPatch) -> None:
    """Evals determinísticos nunca veem chave real: falha cedo se algo tentar usar."""
    for var in ("TYPESAFE_API_KEY", "OPENROUTER_API_KEY"):
        if os.environ.get(var):
            monkeypatch.setenv(var, "chave-falsa-evals")
