"""Testes das tools de chamado (SAC) — sem rede: `httpx.post` é stubado.

Cobrem: payload/headers enviados ao gateway Go, a injeção do `telefone` a partir
do ESTADO (relator nunca vem do LLM), o fluxo em DUAS FASES da abertura
(preparar guarda a proposta no estado → abrir executa exatamente ela) e o
fail-soft em falha de rede.
"""

from __future__ import annotations

import pytest
from langchain_core.messages import AIMessage

from src.agent.nodes import make_tools_node
from src.tools import sac_tools
from src.tools.sac_tools import (
    SAC_TOOLS,
    abrir_chamado_suporte,
    consultar_tipos_de_chamado,
    listar_chamados_abertos,
    preparar_abertura_chamado,
)


_PROPOSTA = {
    "resumo": "App travando",
    "descricao": "Trava ao bipar",
    "categoria": "APLICATIVO",
    "ocorrencia": "LENTIDÃO OU TRAVAMENTOS",
    "prioridade": "ALTA",
    "empresa": "AZAPERS",
    "prazo": 1,
}


class _FakeResp:
    def __init__(self, payload: dict):
        self._payload = payload

    def raise_for_status(self) -> None:  # sempre 2xx neste fake
        return None

    def json(self) -> dict:
        return self._payload


@pytest.fixture
def captura(monkeypatch):
    """Stub de `httpx.post`: registra as chamadas e devolve `resposta`."""
    chamadas: list[dict] = []
    estado = {"resposta": {"status": True}}

    def fake_post(url, json=None, headers=None, timeout=None):  # noqa: A002
        chamadas.append({"url": url, "json": json, "headers": headers, "timeout": timeout})
        return _FakeResp(estado["resposta"])

    monkeypatch.setattr(sac_tools.httpx, "post", fake_post)
    return chamadas, estado


def test_preparar_envia_payload_e_token(captura):
    chamadas, estado = captura
    estado["resposta"] = {"status": True, "proposta": dict(_PROPOSTA)}
    out = preparar_abertura_chamado.invoke(
        {
            "resumo": "App travando",
            "descricao": "Trava ao bipar",
            "categoria": "aplicativo",
            "ocorrencia": "lentidão ou travamentos",
            "prioridade": "ALTA",
            "telefone": "5531983857490",
        }
    )
    assert out["proposta"]["categoria"] == "APLICATIVO"
    enviado = chamadas[-1]
    assert enviado["url"].endswith("/tools/sac/preparar")
    # O header carrega o token das settings — não fixamos o valor para o teste
    # não depender do `.env` local (era a falha ambiental pré-existente).
    from src.config import get_settings

    assert enviado["headers"]["X-Tools-Token"] == get_settings().sac_tools_token
    assert enviado["json"]["telefone"] == "5531983857490"


def test_abrir_executa_a_proposta_injetada(captura):
    """`abrir` não recebe conteúdo do LLM: o payload do /criar É a proposta."""
    chamadas, estado = captura
    estado["resposta"] = {
        "status": True,
        "protocolo": "ZPRS25207690",
        "link": "https://atendimento.azapfy.com.br/chat/x/AZAPERS/ZPRS25207690",
    }
    out = abrir_chamado_suporte.invoke(
        {"proposta": dict(_PROPOSTA), "telefone": "5531983857490"}
    )
    assert out["protocolo"] == "ZPRS25207690"
    enviado = chamadas[-1]
    assert enviado["url"].endswith("/tools/sac/criar")
    assert enviado["json"]["resumo"] == "App travando"
    assert enviado["json"]["categoria"] == "APLICATIVO"
    assert enviado["json"]["grupo_emp"] == "AZAPERS"
    assert enviado["json"]["telefone"] == "5531983857490"


def test_fluxo_duas_fases_no_tools_node(captura):
    """preparar guarda a proposta no estado; abrir consome exatamente ela."""
    chamadas, estado = captura
    node = make_tools_node(SAC_TOOLS)

    # Fase 1: preparar — o dry-run aprovado vira `proposta_chamado` no estado.
    estado["resposta"] = {"status": True, "proposta": dict(_PROPOSTA)}
    ai = AIMessage(
        content="",
        tool_calls=[
            {
                "name": "preparar_abertura_chamado",
                "id": "call-1",
                "args": {
                    "resumo": "App travando",
                    "descricao": "Trava ao bipar",
                    "categoria": "APLICATIVO",
                    "ocorrencia": "LENTIDÃO OU TRAVAMENTOS",
                },
            }
        ],
    )
    out1 = node({"messages": [ai], "telefone": "5531999990000"})
    assert out1["proposta_chamado"] == _PROPOSTA
    # O telefone do payload veio do ESTADO, não do LLM.
    assert chamadas[-1]["json"]["telefone"] == "5531999990000"

    # Fase 2: abrir com args VAZIOS — a proposta e o telefone são injetados.
    estado["resposta"] = {"status": True, "protocolo": "ZP1", "link": "http://x/AZAPERS/ZP1"}
    ai2 = AIMessage(
        content="",
        tool_calls=[{"name": "abrir_chamado_suporte", "id": "call-2", "args": {}}],
    )
    out2 = node(
        {
            "messages": [ai2],
            "telefone": "5531999990000",
            "proposta_chamado": dict(_PROPOSTA),
        }
    )
    assert chamadas[-1]["json"]["resumo"] == "App travando"
    assert chamadas[-1]["json"]["telefone"] == "5531999990000"
    # Abertura bem-sucedida CONSOME a proposta (não sobra pra reuso acidental).
    assert out2["proposta_chamado"] is None
    tool_msgs = out2["messages"]
    assert tool_msgs and tool_msgs[0].name == "abrir_chamado_suporte"
    assert "ZP1" in tool_msgs[0].content


def test_abrir_sem_proposta_preparada_e_recusado(captura):
    """Sem dry-run aprovado no estado, a abertura nem chega ao gateway."""
    chamadas, _ = captura
    node = make_tools_node(SAC_TOOLS)
    ai = AIMessage(
        content="",
        tool_calls=[{"name": "abrir_chamado_suporte", "id": "call-1", "args": {}}],
    )
    out = node({"messages": [ai], "telefone": "5531999990000"})
    content = out["messages"][0].content
    assert "recusado_pela_politica" in content
    assert "preparar_abertura_chamado" in content
    assert not chamadas  # nenhuma chamada HTTP aconteceu


def test_listar_e_consultar_tipos_endpoints(captura):
    chamadas, estado = captura
    estado["resposta"] = {"status": True, "total": 0, "chamados": []}
    listar_chamados_abertos.invoke({"telefone": "5531999990000"})
    assert chamadas[-1]["url"].endswith("/tools/sac/listar")

    estado["resposta"] = {"status": True, "categorias": [], "ocorrencias": []}
    consultar_tipos_de_chamado.invoke({"telefone": "5531999990000"})
    assert chamadas[-1]["url"].endswith("/tools/sac/tipos")


def test_falha_de_rede_retorna_status_false(monkeypatch):
    def boom(*_args, **_kwargs):
        raise RuntimeError("conexão recusada")

    monkeypatch.setattr(sac_tools.httpx, "post", boom)
    out = listar_chamados_abertos.invoke({"telefone": "5531999990000"})
    assert out["status"] is False
    assert "erro" in out


def test_schemas_visiveis_ao_modelo():
    """Injetados fora do schema: telefone em todas; TUDO em `abrir` (2 fases)."""
    campos_preparar = preparar_abertura_chamado.tool_call_schema.model_fields
    assert "telefone" not in campos_preparar
    assert "resumo" in campos_preparar and "categoria" in campos_preparar

    campos_abrir = abrir_chamado_suporte.tool_call_schema.model_fields
    assert "telefone" not in campos_abrir
    assert "proposta" not in campos_abrir
    assert not campos_abrir  # o modelo não tem NENHUM argumento para abrir
