"""Cenários de conversa como testes pytest (`pytest -m evals_pr evals/`).

Dois níveis: (1) todo YAML carrega e valida; (2) cada cenário executa no grafo
real (LLM scriptado + `FakeJEV` + gateway falso). Um cenário com `xfail: "motivo"`
descreve o comportamento correto que a Sofia ainda não tem: roda como xfail
ESTRITO — se passar, o teste falha e o marcador deve sair.
"""

from __future__ import annotations

import pytest

from evals.conversa import runner
from evals.formatos import CenarioConversa, ErroFormato, carregar_taxonomia

pytestmark = pytest.mark.evals_pr

# Carga no import para parametrizar; um YAML inválido NÃO derruba a coleção:
# vira falha explícita em test_todos_os_yamls_sao_validos.
_ERRO_CARGA: str | None = None
try:
    _CENARIOS: list[CenarioConversa] = runner.carregar_cenarios(taxonomia=carregar_taxonomia())
except (ErroFormato, ValueError) as _e:
    _ERRO_CARGA = str(_e)
    _CENARIOS = []


def _param(cenario: CenarioConversa) -> object:
    marcas = []
    if cenario.xfail:
        marcas.append(pytest.mark.xfail(reason=cenario.xfail, strict=True))
    return pytest.param(cenario, id=cenario.id, marks=marcas)


def test_todos_os_yamls_sao_validos() -> None:
    assert _ERRO_CARGA is None, _ERRO_CARGA
    assert _CENARIOS, "nenhum cenário em evals/conversa/"
    assert len({c.id for c in _CENARIOS}) == len(_CENARIOS)
    assert any(c.arquivo is not None and c.arquivo.name == "TEMPLATE.yaml" for c in _CENARIOS)


def test_todo_cenario_tem_negativo() -> None:
    """Regra da skill evals-agente: um cenário sempre inclui o que NÃO pode acontecer."""
    for c in _CENARIOS:
        tem_negativo = any(
            "nao_chama" in t.espera or "nao_contem" in t.espera or t.espera.get("bloqueado") is True
            for t in c.turnos
        )
        assert tem_negativo, f"{c.id} não tem assert negativo (nao_chama/nao_contem/bloqueado)"


@pytest.mark.parametrize("cenario", [_param(c) for c in _CENARIOS])
def test_cenario_executa(cenario: CenarioConversa) -> None:
    resultado = runner.executar(cenario)
    assert resultado.passou, "\n".join(resultado.falhas)
    assert resultado.turnos_executados == len(cenario.turnos)
