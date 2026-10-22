"""Prazos do turno alinhados (E3.7): defaults novos e ordem validada no boot.

Regra: BRAIN_TIMEOUT (90s) > TURNO_TETO (80s) > BRAIN_TURNO_PRAZO (70s)
> INFODESK_TOOLS_TIMEOUT (30s) + folga do JEV. O gateway Go confere a metade
dele (`backend/internal/config`); o cérebro confere a dele e, quando o ambiente
traz os prazos do gateway, a cadeia inteira.
"""

from __future__ import annotations

import pytest

from src.agent.graph import build_graph
from src.config import (
    ConfigPrazosInvalida,
    Settings,
    duracao_em_segundos,
    get_settings,
    validar_prazos,
)
from tests.fakes import FakeJEV, fake_llm, resp_jev, sem_rag, servico_fake

_VARS = (
    "BRAIN_TURNO_PRAZO",
    "INFODESK_TOOLS_TIMEOUT",
    "BRAIN_TIMEOUT",
    "TURNO_TETO",
    "LLM_TIMEOUT",
)


@pytest.fixture(autouse=True)
def _ambiente_limpo(monkeypatch, tmp_path):
    """Só os defaults do código: nem o ambiente nem um `.env` local mascaram."""
    for nome in _VARS:
        monkeypatch.delenv(nome, raising=False)
    monkeypatch.chdir(tmp_path)  # `.env` relativo ao cwd
    get_settings.cache_clear()
    yield
    get_settings.cache_clear()


def _settings(monkeypatch, **env: str) -> Settings:
    for nome, valor in env.items():
        monkeypatch.setenv(nome, valor)
    return Settings()  # type: ignore[call-arg]


def test_defaults_novos():
    s = Settings()  # type: ignore[call-arg]
    assert s.brain_turno_prazo == 70.0
    assert s.infodesk_tools_timeout == 30.0
    assert s.brain_timeout_gateway == "" and s.turno_teto_gateway == ""
    validar_prazos(s)  # os defaults são válidos


def test_defaults_do_gateway_no_ambiente_fecham_a_cadeia(monkeypatch):
    s = _settings(monkeypatch, BRAIN_TIMEOUT="90s", TURNO_TETO="80s")
    validar_prazos(s)


@pytest.mark.parametrize(
    ("env", "cita"),
    [
        # a tool (30s) + folga do JEV não cabe no turno
        ({"BRAIN_TURNO_PRAZO": "31", "INFODESK_TOOLS_TIMEOUT": "30"}, "INFODESK_TOOLS_TIMEOUT"),
        ({"BRAIN_TURNO_PRAZO": "30", "INFODESK_TOOLS_TIMEOUT": "30"}, "INFODESK_TOOLS_TIMEOUT"),
        # o gateway desistiria antes do cérebro
        ({"TURNO_TETO": "70s"}, "TURNO_TETO"),
        ({"TURNO_TETO": "60s"}, "TURNO_TETO"),
        # BRAIN_TIMEOUT não pode ser menor/igual ao teto do turno
        ({"TURNO_TETO": "80s", "BRAIN_TIMEOUT": "80s"}, "BRAIN_TIMEOUT"),
        ({"TURNO_TETO": "80s", "BRAIN_TIMEOUT": "60s"}, "BRAIN_TIMEOUT"),
        # só BRAIN_TIMEOUT presente: ainda tem de passar do prazo do cérebro
        ({"BRAIN_TIMEOUT": "60s"}, "BRAIN_TIMEOUT"),
        # formato Go inválido cita a variável
        ({"TURNO_TETO": "oitenta"}, "TURNO_TETO"),
    ],
)
def test_ordem_quebrada_vira_erro_de_configuracao_claro(monkeypatch, env, cita):
    s = _settings(monkeypatch, **env)
    with pytest.raises(ConfigPrazosInvalida, match=cita):
        validar_prazos(s)


def test_folga_do_jev_entra_na_conta(monkeypatch):
    s = _settings(monkeypatch, BRAIN_TURNO_PRAZO="33", INFODESK_TOOLS_TIMEOUT="30")
    validar_prazos(s, folga_jev=2.0)
    with pytest.raises(ConfigPrazosInvalida, match="folga do JEV"):
        validar_prazos(s, folga_jev=3.0)


@pytest.mark.parametrize(
    ("texto", "segundos"),
    [("90s", 90.0), ("1m30s", 90.0), ("1500ms", 1.5), ("2m", 120.0), ("45", 45.0), ("1h", 3600.0)],
)
def test_duracao_formato_go(texto, segundos):
    assert duracao_em_segundos(texto, "X") == pytest.approx(segundos)


def test_build_graph_recusa_prazos_fora_de_ordem_no_boot(monkeypatch):
    monkeypatch.setenv("BRAIN_TURNO_PRAZO", "20")
    get_settings.cache_clear()
    with pytest.raises(ConfigPrazosInvalida, match="BRAIN_TURNO_PRAZO"):
        build_graph(llm=fake_llm([]), tools=[], buscar_chunks=sem_rag)


def test_build_graph_usa_o_jev_timeout_do_classificador(monkeypatch):
    """BRAIN_TURNO_PRAZO=33 > 30 + 2 (JEV_TIMEOUT default), mas não > 30 + 5."""
    monkeypatch.setenv("BRAIN_TURNO_PRAZO", "33")
    get_settings.cache_clear()
    servico = servico_fake(FakeJEV(padrao=resp_jev()))
    build_graph(llm=fake_llm([]), tools=[], buscar_chunks=sem_rag, classificador=servico)
    servico.config.jev_timeout = 5.0
    with pytest.raises(ConfigPrazosInvalida, match="folga do JEV"):
        build_graph(llm=fake_llm([]), tools=[], buscar_chunks=sem_rag, classificador=servico)
