"""Testes das tools de chamado (SAC) — sem rede: `httpx.post` é stubado.

Cobrem: payload/headers enviados ao gateway Go, a injeção do `telefone` a partir
do ESTADO (relator nunca vem do LLM), o fluxo em DUAS FASES da abertura
(preparar guarda a proposta no estado → abrir executa exatamente ela) e o
fail-soft em falha de rede.
"""

from __future__ import annotations

import pytest
from langchain_core.messages import AIMessage, HumanMessage

from src.agent.nodes import make_tools_node
from src.agent.tool_policy import carimbar_proposta
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

    def fake_post(url, json=None, headers=None, timeout=None):
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
    out = abrir_chamado_suporte.invoke({"proposta": dict(_PROPOSTA), "telefone": "5531983857490"})
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
    # A proposta guardada é a do gateway + o carimbo de validade/turno (ZB-1).
    guardada = out1["proposta_chamado"]
    assert {k: v for k, v in guardada.items() if k not in ("criada_em", "turno")} == _PROPOSTA
    assert guardada["turno"] == 0 and guardada["criada_em"]
    # O telefone do payload veio do ESTADO, não do LLM.
    assert chamadas[-1]["json"]["telefone"] == "5531999990000"

    # Fase 2 (turno seguinte, cliente confirmou): abrir com args VAZIOS — a
    # proposta e o telefone são injetados.
    estado["resposta"] = {"status": True, "protocolo": "ZP1", "link": "http://x/AZAPERS/ZP1"}
    ai2 = AIMessage(
        content="",
        tool_calls=[{"name": "abrir_chamado_suporte", "id": "call-2", "args": {}}],
    )
    out2 = node(
        {
            "messages": [HumanMessage(content="sim"), ai2],
            "telefone": "5531999990000",
            "proposta_chamado": guardada,
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

    for t in SAC_TOOLS:
        assert "conversation_id" not in t.tool_call_schema.model_fields, t.name


# ---------------------------------------------------------------------------
# ZC-2b (ponta Python do ZA-3): toda chamada ao gateway leva o conversation_id
# da requisição — injetado do ESTADO (nunca do LLM), para o log de erro do SAC
# no gateway sair com o id da conversa.
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "nome, args",
    [
        ("consultar_tipos_de_chamado", {}),
        ("listar_chamados_abertos", {}),
        (
            "preparar_abertura_chamado",
            {"resumo": "r", "descricao": "d", "categoria": "c", "ocorrencia": "o"},
        ),
        ("abrir_chamado_suporte", {}),
    ],
)
def test_tools_node_injeta_conversation_id_da_sessao(captura, nome, args):
    chamadas, estado = captura
    estado["resposta"] = {"status": True, "proposta": dict(_PROPOSTA)}
    node = make_tools_node(SAC_TOOLS)
    # O modelo tenta "escolher" outro id: a injeção sobrescreve.
    ai = AIMessage(
        content="",
        tool_calls=[{"name": nome, "id": "call-1", "args": {**args, "conversation_id": "999"}}],
    )
    node(
        {
            # "sim" no turno seguinte à proposta carimbada: `abrir` passa na
            # política (ZB-1) e chega ao gateway como as demais.
            "messages": [HumanMessage(content="sim"), ai],
            "telefone": "5531999990000",
            "conversation_id": "45",
            "proposta_chamado": carimbar_proposta(_PROPOSTA, {}),
        }
    )
    assert chamadas, nome
    assert chamadas[-1]["json"]["conversation_id"] == "45"
    assert chamadas[-1]["json"]["telefone"] == "5531999990000"


def test_sem_conversation_id_no_estado_envia_vazio(captura):
    """Harness de dev (Chainlit) não tem conversa do Chatwoot: vai vazio, não quebra."""
    chamadas, estado = captura
    estado["resposta"] = {"status": True, "total": 0, "chamados": []}
    node = make_tools_node(SAC_TOOLS)
    ai = AIMessage(
        content="",
        tool_calls=[{"name": "listar_chamados_abertos", "id": "call-1", "args": {}}],
    )
    node({"messages": [ai], "telefone": "5531999990000"})
    assert chamadas[-1]["json"]["conversation_id"] == ""


# ---------------------------------------------------------------------------
# ZB-2: `novo_chamado` — o único argumento novo visível ao modelo em
# `preparar`; vai ao gateway no dry-run e, pela proposta, na abertura.
# ---------------------------------------------------------------------------


def test_preparar_envia_novo_chamado_false_por_padrao(captura):
    chamadas, estado = captura
    estado["resposta"] = {"status": True, "proposta": dict(_PROPOSTA)}
    preparar_abertura_chamado.invoke(
        {"resumo": "r", "descricao": "d", "categoria": "c", "ocorrencia": "o", "telefone": "1"}
    )
    assert chamadas[-1]["json"]["novo_chamado"] is False
    preparar_abertura_chamado.invoke(
        {
            "resumo": "r",
            "descricao": "d",
            "categoria": "c",
            "ocorrencia": "o",
            "novo_chamado": True,
            "telefone": "1",
        }
    )
    assert chamadas[-1]["json"]["novo_chamado"] is True


def test_abrir_repassa_novo_chamado_da_proposta(captura):
    chamadas, estado = captura
    estado["resposta"] = {"status": True, "protocolo": "ZP1", "link": "http://x"}
    abrir_chamado_suporte.invoke({"proposta": {**_PROPOSTA, "novo_chamado": True}, "telefone": "1"})
    assert chamadas[-1]["json"]["novo_chamado"] is True
    abrir_chamado_suporte.invoke({"proposta": dict(_PROPOSTA), "telefone": "1"})
    assert chamadas[-1]["json"]["novo_chamado"] is False


def test_novo_chamado_e_visivel_ao_modelo_so_em_preparar():
    assert "novo_chamado" in preparar_abertura_chamado.tool_call_schema.model_fields
    assert not abrir_chamado_suporte.tool_call_schema.model_fields


def test_tools_node_novo_chamado_sem_pedido_do_cliente_nao_chega_ao_gateway(captura):
    """A política (ZB-2) barra `novo_chamado=true` inventado pelo modelo antes
    de qualquer chamada HTTP."""
    chamadas, _ = captura
    node = make_tools_node(SAC_TOOLS)
    ai = AIMessage(
        content="",
        tool_calls=[
            {
                "name": "preparar_abertura_chamado",
                "id": "call-1",
                "args": {
                    "resumo": "r",
                    "descricao": "d",
                    "categoria": "c",
                    "ocorrencia": "o",
                    "novo_chamado": True,
                },
            }
        ],
    )
    out = node({"messages": [HumanMessage(content="o app trava"), ai], "telefone": "1"})
    assert "recusado_pela_politica" in out["messages"][0].content
    assert not chamadas
