"""Testes da política de autorização de tools ("o LLM propõe, o código dispõe").

As entradas de produção de `POLITICAS` são de E2.3; aqui o mecanismo é
exercitado com as políticas de teste de `tests/fakes.py` (instaladas por
monkeypatch): injeção de args de sessão, precondição de duas fases,
confirmação explícita checada por código, carimbo e validade da proposta.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest
from langchain_core.messages import AIMessage, HumanMessage

from src.agent import tool_policy
from src.agent.nodes import make_tools_node
from src.agent.tool_policy import (
    PROPOSTA_VALIDADE,
    PoliticaTool,
    aplicar_politica,
    carimbar_proposta,
    eh_confirmacao_explicita,
)
from src.tools import efeitos
from tests.fakes import (
    CHAMADAS,
    registrar_duas_fases_de_teste,
    tool_abrir_de_teste,
    tool_de_teste,
)

_CONTATO = {"forma": "cpf", "valor_mascarado": "***.***.789-**"}


@pytest.fixture
def politicas(monkeypatch):
    registrar_duas_fases_de_teste(monkeypatch)


# ---------------------------------------------------------------------------
# Estado de produção nesta etapa
# ---------------------------------------------------------------------------


def test_politicas_de_producao_cobrem_exatamente_as_tools_expostas():
    """Tool exposta sem política (ou política órfã) é bug de segurança."""
    from src.agent.graph import get_default_tools

    assert set(tool_policy.POLITICAS) == {t.name for t in get_default_tools()}
    assert len(tool_policy.POLITICAS) == 8


def test_tool_sem_politica_passa_intocada():
    args, erro = aplicar_politica("qualquer_tool", {"a": 1}, {})
    assert (args, erro) == ({"a": 1}, None)


# ---------------------------------------------------------------------------
# aplicar_politica — injeção sobrescreve o LLM; precondição recusa
# ---------------------------------------------------------------------------


def test_injecao_sobrescreve_arg_alucinado_pelo_llm(politicas):
    """Mesmo que o modelo alucine um arg de sessão, a sessão manda."""
    state = {"telefone": "5531999990000", "conversation_id": "conv-1", "contato": _CONTATO}
    args, erro = aplicar_politica(
        "tool_de_teste",
        {"x": "oi", "telefone": "5500000000000", "conversation_id": "outra"},
        state,
    )
    assert erro is None
    assert args == {"x": "oi", "telefone": "5531999990000", "conversation_id": "conv-1"}


def test_injecao_sem_valor_na_sessao_da_vazio(politicas):
    args, erro = aplicar_politica("tool_de_teste", {"x": "oi"}, {})
    assert erro is None and args["telefone"] == "" and args["conversation_id"] == ""


def test_validador_recusa_arg_do_llm(monkeypatch):
    """`validar` barra um argumento legítimo do modelo quando ele não bate
    com a sessão — a tool não executa e o motivo vira o erro."""

    def _so_digitos(valor, state):
        return None if str(valor).isdigit() else "numero precisa ter só dígitos"

    monkeypatch.setitem(
        tool_policy.POLITICAS, "tool_de_teste", PoliticaTool(validar={"x": _so_digitos})
    )
    _, erro = aplicar_politica("tool_de_teste", {"x": "48a21"}, {})
    assert erro == "numero precisa ter só dígitos"
    args, erro = aplicar_politica("tool_de_teste", {"x": "4821"}, {})
    assert erro is None and args == {"x": "4821"}


def test_precondicao_roda_antes_de_validar_e_injetar(monkeypatch):
    monkeypatch.setitem(
        tool_policy.POLITICAS,
        "tool_de_teste",
        PoliticaTool(
            injetar={"telefone": lambda s: "nunca"},
            validar={"x": lambda v, s: "nunca"},
            exigir=lambda s: "precondição falhou",
        ),
    )
    args, erro = aplicar_politica("tool_de_teste", {"x": "1"}, {})
    assert erro == "precondição falhou"
    assert "telefone" not in args  # nada injetado numa chamada recusada


def test_abrir_sem_proposta_preparada_e_recusado(politicas):
    """Duas fases: abrir sem dry-run aprovado no estado não é representável."""
    _, erro = aplicar_politica("tool_abrir_de_teste", {}, {"contato": _CONTATO})
    assert erro is not None
    assert "preparar_abertura_chamado" in erro


def test_abrir_injeta_a_proposta_do_estado(politicas):
    """A abertura executa a proposta preparada — mesmo que o modelo tente
    passar outro conteúdo, a injeção sobrescreve. (Proposta carimbada no
    turno anterior e cliente confirmando neste.)"""
    proposta = carimbar_proposta({"titulo": "Sem conexão", "categoria": "sem_conexao"}, {})
    state = {
        "contato": _CONTATO,
        "telefone": "5531999990000",
        "proposta_chamado": proposta,
        "messages": [HumanMessage(content="sim")],
    }
    args, erro = aplicar_politica(
        "tool_abrir_de_teste", {"proposta": {"titulo": "OUTRA COISA alucinada"}}, state
    )
    assert erro is None
    assert args["proposta"] == proposta
    assert args["telefone"] == "5531999990000"


# ---------------------------------------------------------------------------
# Confirmação explícita checada por código (nunca pelo modelo)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "texto",
    [
        "sim",
        "Sim!",
        "SIM",
        "ok",
        "Ok.",
        "pode abrir",
        "Pode abrir, sim",
        "pode",
        "confirmo",
        "Pode abrir 👍",
        "tá bom",
        "beleza",
        "sim, por favor",
        "pode abrir o chamado",
        "sim, Sofia",
        "pode abrir, Sofia",
    ],
)
def test_confirmacao_explicita_aceita(texto):
    assert eh_confirmacao_explicita(texto) is True


@pytest.mark.parametrize(
    "texto",
    [
        "Esse",  # deítico: pode apontar um chamado existente
        "4821",  # número de chamado
        "isso",
        "esse mesmo",
        "não",
        "sim, mas muda a prioridade",
        "pode abrir? qual o prazo?",
        "obrigado",
        "",
        None,
        "sim e também abre outro pro roteador",
        "sim maria",  # dirigido a outra pessoa não é confirmação à Sofia
    ],
)
def test_confirmacao_explicita_recusa_o_que_nao_e_so_confirmacao(texto):
    assert eh_confirmacao_explicita(texto) is False


def test_confirmacoes_nao_contem_deiticos():
    assert not {"esse", "isso", "aquele", "esse mesmo"} & tool_policy.CONFIRMACOES_EXPLICITAS


def _estado_abrir(proposta: dict, mensagens: list) -> dict:
    return {
        "contato": _CONTATO,
        "telefone": "5531999990000",
        "proposta_chamado": proposta,
        "messages": mensagens,
    }


def test_carimbar_proposta_registra_criada_em_utc_e_turno():
    agora = datetime(2026, 10, 6, 12, 0, tzinfo=UTC)
    msgs = [HumanMessage(content="a"), AIMessage(content="b"), HumanMessage(content="c")]
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": msgs}, agora=agora)
    assert proposta == {"titulo": "x", "criada_em": "2026-10-06T12:00:00+00:00", "turno": 2}


def test_carimbar_proposta_nao_muta_a_original():
    original = {"titulo": "x"}
    carimbar_proposta(original, {})
    assert original == {"titulo": "x"}


def test_abrir_recusado_quando_proposta_venceu(politicas):
    velha = carimbar_proposta(
        {"titulo": "x"}, {}, agora=datetime.now(UTC) - PROPOSTA_VALIDADE - timedelta(seconds=1)
    )
    _, erro = aplicar_politica(
        "tool_abrir_de_teste", {}, _estado_abrir(velha, [HumanMessage(content="sim")])
    )
    assert erro is not None and "venceu" in erro


def test_abrir_recusado_no_mesmo_turno_da_proposta(politicas):
    msgs = [HumanMessage(content="abre um chamado")]
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": msgs})
    _, erro = aplicar_politica("tool_abrir_de_teste", {}, _estado_abrir(proposta, msgs))
    assert erro is not None and "NESTE turno" in erro


def test_abrir_recusado_quando_a_conversa_seguiu_sem_confirmar(politicas):
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [HumanMessage(content="1"), HumanMessage(content="2"), HumanMessage(content="sim")]
    _, erro = aplicar_politica("tool_abrir_de_teste", {}, _estado_abrir(proposta, msgs))
    assert erro is not None and "SEGUINTE" in erro


@pytest.mark.parametrize("mensagem", ["Esse", "4821", "não", "sim, mas muda a prioridade"])
def test_abrir_recusado_sem_confirmacao_explicita_no_turno_seguinte(politicas, mensagem):
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [HumanMessage(content="1"), HumanMessage(content=mensagem)]
    _, erro = aplicar_politica("tool_abrir_de_teste", {}, _estado_abrir(proposta, msgs))
    assert erro is not None and "confirmação explícita" in erro


def test_abrir_recusado_quando_proposta_sem_carimbo(politicas):
    _, erro = aplicar_politica(
        "tool_abrir_de_teste", {}, _estado_abrir({"titulo": "x"}, [HumanMessage(content="sim")])
    )
    assert erro is not None and "preparar_abertura_chamado" in erro


def test_abrir_recusado_quando_carimbo_e_invalido(politicas):
    proposta = {"titulo": "x", "criada_em": "ontem", "turno": 0}
    _, erro = aplicar_politica(
        "tool_abrir_de_teste", {}, _estado_abrir(proposta, [HumanMessage(content="sim")])
    )
    assert erro is not None and "versão anterior" in erro


def test_abrir_aceito_com_proposta_valida_e_sim_no_turno_seguinte(politicas):
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [
        HumanMessage(content="1"),
        AIMessage(content="Posso abrir?"),
        HumanMessage(content="Sim!"),
    ]
    args, erro = aplicar_politica("tool_abrir_de_teste", {}, _estado_abrir(proposta, msgs))
    assert erro is None and args["proposta"] == proposta


def test_confirmacao_tolera_content_em_blocos(politicas):
    proposta = carimbar_proposta({"titulo": "x"}, {"messages": [HumanMessage(content="1")]})
    msgs = [HumanMessage(content="1"), HumanMessage(content=[{"type": "text", "text": "sim"}])]
    _, erro = aplicar_politica("tool_abrir_de_teste", {}, _estado_abrir(proposta, msgs))
    assert erro is None


# ---------------------------------------------------------------------------
# tools_node — a recusa vira ToolMessage e a tool NÃO executa
# ---------------------------------------------------------------------------


def test_tools_node_recusa_da_politica_vira_toolmessage_sem_executar(politicas):
    node = make_tools_node([tool_abrir_de_teste])
    ai = AIMessage(
        content="", tool_calls=[{"id": "tc1", "name": "tool_abrir_de_teste", "args": {}}]
    )
    out = node({"messages": [HumanMessage(content="abre"), ai], "contato": _CONTATO})
    content = out["messages"][0].content
    assert "recusado_pela_politica" in content
    assert "preparar_abertura_chamado" in content
    assert CHAMADAS == []  # a tool não executou
    assert "proposta_chamado" not in out  # nenhum efeito aplicado


def test_tools_node_schema_do_modelo_nao_expoe_args_injetados():
    for t in (tool_de_teste, tool_abrir_de_teste):
        props = set(t.tool_call_schema.model_json_schema().get("properties", {}))
        assert not props & {"telefone", "conversation_id", "proposta"}, t.name


# ===========================================================================
# E2.3 — a matriz de políticas das 6 tools do Infodesk (01, seção D)
# ===========================================================================

_SESSAO_REAL = {"telefone": "+5531999990001", "conversation_id": "conv-e23"}
_CLASSIF = {
    "intencao": "reclamacao_problema",
    "categoria": "sem_conexao",
    "prioridade": "alta",
    "confianca": 0.9,
    "origem": "jev",
}
_CONTATO_LOOKUP = {"forma": "telefone", "valor_mascarado": "Maria S.", "cliente_id": "c-1"}
_CONTATO_CPF_REAL = {"forma": "cpf", "valor_mascarado": "***.***.789-**"}


def _estado(**extra) -> dict:
    base = {
        **_SESSAO_REAL,
        "classificacao": _CLASSIF,
        "contato": _CONTATO_CPF_REAL,
        "messages": [HumanMessage(content="minha internet caiu")],
    }
    base.update(extra)
    return base


def _proposta_pronta(turno_mensagens: int = 1) -> dict:
    msgs = [HumanMessage(content=str(i)) for i in range(turno_mensagens)]
    return carimbar_proposta(
        {"titulo": "Sem conexão", "descricao": "d", "categoria": "sem_conexao"},
        {"messages": msgs},
    )


@pytest.mark.parametrize(
    ("nome", "args_de_sessao"),
    [
        ("informar_identificacao", {"telefone", "conversation_id"}),
        (
            "preparar_abertura_chamado",
            {"telefone", "conversation_id", "classificacao", "identificacao"},
        ),
        ("abrir_chamado", {"telefone", "conversation_id", "proposta"}),
        ("consultar_status_chamado", {"telefone", "conversation_id", "identificacao"}),
        ("listar_meus_chamados", {"telefone", "conversation_id"}),
        ("transferir_para_atendente", {"telefone", "conversation_id", "classificacao"}),
    ],
)
def test_matriz_injeta_todo_arg_de_sessao_e_sobrescreve_o_do_llm(nome, args_de_sessao):
    """Cada tool injeta exatamente os args de sessão da seção D, e o que o modelo
    tentar passar nesses nomes é sobrescrito pelo estado."""
    assert set(tool_policy.POLITICAS[nome].injetar) == args_de_sessao
    estado = _estado(
        proposta_chamado=_proposta_pronta(),
        messages=[HumanMessage(content="1"), HumanMessage(content="sim")],
    )
    forjado = {a: "FORJADO" for a in args_de_sessao}
    legitimos = {"numero": "1", "motivo": "pedido_cliente", "titulo": "t", "descricao": "d"}
    args, erro = aplicar_politica(nome, {**forjado, **legitimos}, estado)
    assert erro is None, erro
    assert all(args[a] != "FORJADO" for a in args_de_sessao)
    assert args["telefone"] == "+5531999990001" and args["conversation_id"] == "conv-e23"


def test_classificacao_injetada_so_tem_as_cinco_chaves_do_contrato():
    args, _ = aplicar_politica(
        "transferir_para_atendente",
        {"motivo": "pedido_cliente"},
        _estado(classificacao={**_CLASSIF, "extra": "x"}),
    )
    assert args["classificacao"] == _CLASSIF


def test_identificacao_injetada_e_so_a_visao_mascarada():
    contato = {**_CONTATO_CPF_REAL, "cliente_id": "c1", "nome_cadastro": "Maria da Silva"}
    args, _ = aplicar_politica(
        "consultar_status_chamado", {"numero": "48213"}, _estado(contato=contato)
    )
    assert args["identificacao"] == {"forma": "cpf", "valor_mascarado": "***.***.789-**"}


def test_identificacao_do_estado_vale_quando_o_gateway_nao_mandou_contato():
    args, _ = aplicar_politica(
        "consultar_status_chamado",
        {"numero": "48213"},
        _estado(contato=None, identificacao={"forma": "contrato", "valor_mascarado": "******456"}),
    )
    assert args["identificacao"] == {"forma": "contrato", "valor_mascarado": "******456"}


def test_sem_identificacao_o_arg_injetado_e_none():
    args, _ = aplicar_politica(
        "consultar_status_chamado", {"numero": "48213"}, _estado(contato=None)
    )
    assert args["identificacao"] is None


# --- informar_identificacao: cpf (DV) e contrato (regex) -------------------


@pytest.mark.parametrize("cpf", ["12345678900", "111.111.111-11", "123", "abc"])
def test_informar_recusa_cpf_invalido_sem_chegar_na_tool(cpf):
    _, erro = aplicar_politica("informar_identificacao", {"cpf": cpf}, _estado())
    assert erro is not None and "CPF inválido" in erro
    assert cpf not in erro  # a recusa não ecoa o dado do cliente


@pytest.mark.parametrize("cpf", ["12345678909", "123.456.789-09", ""])
def test_informar_aceita_cpf_valido_ou_ausente(cpf):
    _, erro = aplicar_politica(
        "informar_identificacao", {"cpf": cpf, "contrato": "4821"}, _estado()
    )
    assert erro is None


@pytest.mark.parametrize("contrato", ["12", "12a456", "1234567890123", "ab-cd"])
def test_informar_recusa_contrato_fora_da_regex(contrato, monkeypatch):
    monkeypatch.delenv("IDENT_CONTRATO_RE", raising=False)
    _, erro = aplicar_politica("informar_identificacao", {"contrato": contrato}, _estado())
    assert erro is not None and "contrato inválido" in erro


def test_informar_respeita_a_regex_configurada(monkeypatch):
    monkeypatch.setenv("IDENT_CONTRATO_RE", r"^INF-\d{6}$")
    ok = aplicar_politica("informar_identificacao", {"contrato": "INF-123456"}, _estado())
    ruim = aplicar_politica("informar_identificacao", {"contrato": "123456"}, _estado())
    assert ok[1] is None and ruim[1] is not None


# --- preparar: identificado, categoria, protocolo, tamanhos ----------------


def _preparar(estado: dict, **args):
    base = {"titulo": "Sem conexão", "descricao": "Cliente sem sinal"}
    return aplicar_politica("preparar_abertura_chamado", {**base, **args}, estado)


def test_preparar_aceita_cliente_identificado_por_cpf_com_categoria():
    args, erro = _preparar(_estado())
    assert erro is None
    assert args["classificacao"] == _CLASSIF
    assert args["identificacao"] == _CONTATO_CPF_REAL


def test_preparar_recusa_cliente_nao_identificado():
    _, erro = _preparar(_estado(contato=None))
    assert erro is not None and "informar_identificacao" in erro


def test_preparar_aceita_identificado_por_lookup_do_telefone_sem_cpf(monkeypatch):
    """ADR 0004 (revisão): `contato.forma == "telefone"` satisfaz a precondição."""
    monkeypatch.delenv("IDENT_EXIGIR_CPF", raising=False)
    args, erro = _preparar(_estado(contato=_CONTATO_LOOKUP))
    assert erro is None
    assert args["identificacao"] == {"forma": "telefone", "valor_mascarado": "Maria S."}


def test_preparar_exige_cpf_quando_ident_exigir_cpf_e_so_ha_lookup(monkeypatch):
    monkeypatch.setenv("IDENT_EXIGIR_CPF", "true")
    _, erro = _preparar(_estado(contato=_CONTATO_LOOKUP))
    assert erro is not None and "informar_identificacao" in erro


def test_preparar_com_ident_exigir_cpf_aceita_cpf_informado_na_conversa(monkeypatch):
    monkeypatch.setenv("IDENT_EXIGIR_CPF", "true")
    estado = _estado(
        contato=_CONTATO_LOOKUP, identificacao={"forma": "cpf", "valor_mascarado": "***.***.789-**"}
    )
    assert _preparar(estado)[1] is None


@pytest.mark.parametrize("classificacao", [None, {}, {"categoria": None}, {"categoria": ""}])
def test_preparar_recusa_sem_categoria(classificacao):
    _, erro = _preparar(_estado(classificacao=classificacao))
    assert erro is not None and "esclarecimento" in erro


def test_preparar_recusa_categoria_indefinida_por_default(monkeypatch):
    monkeypatch.delenv("JEV_ACAO_INDEFINIDA", raising=False)
    _, erro = _preparar(_estado(classificacao={**_CLASSIF, "categoria": "indefinida"}))
    assert erro is not None and "esclarecimento" in erro


def test_preparar_aceita_categoria_indefinida_quando_a_triagem_e_a_hipotese(monkeypatch):
    """TODO(P11): com `JEV_ACAO_INDEFINIDA=triagem` o chamado vai à mesa de triagem."""
    monkeypatch.setenv("JEV_ACAO_INDEFINIDA", "triagem")
    assert _preparar(_estado(classificacao={**_CLASSIF, "categoria": "indefinida"}))[1] is None


def test_preparar_valida_a_identificacao_antes_da_categoria():
    _, erro = _preparar(_estado(contato=None, classificacao=None))
    assert "informar_identificacao" in erro


@pytest.mark.parametrize(
    "mensagem",
    [
        "como está o chamado 48213?",
        "o protocolo 123456 ainda não andou",
        "ticket #4821 sem resposta",
        "meu chamado nº 48213",
        "Chamado Número 48213",
        "ordem de serviço 9981",
        "o protocolo é 123456",
        "meu chamado de número 4821",
        "chamado número 4.821",
        "protocolo 20240001234",
        "protocolo: 2024-0001",
        "ticket 77123",
    ],
)
def test_preparar_recusa_quando_o_cliente_cita_um_chamado_existente(mensagem):
    _, erro = _preparar(_estado(messages=[HumanMessage(content=mensagem)]))
    assert erro is not None and "consultar_status_chamado" in erro


@pytest.mark.parametrize(
    "mensagem",
    [
        "minha internet caiu às 3 da tarde",
        "já reiniciei 2 vezes",
        "moro na rua 25, casa 4821",
        "tenho os 200 mega e não funciona",
        "Os 150 clientes da rua estão sem internet",
        "meu plano de 500 mega os 500 não chegam",
        "abri um chamado ontem e ninguém ligou",
        "o chamado está parado há 3 dias",
    ],
)
def test_preparar_nao_confunde_numero_solto_com_protocolo(mensagem):
    assert _preparar(_estado(messages=[HumanMessage(content=mensagem)]))[1] is None


@pytest.mark.parametrize(
    ("campo", "valor"),
    [
        ("titulo", ""),
        ("titulo", "x" * 121),
        ("titulo", "linha 1\nlinha 2"),
        ("descricao", ""),
        ("descricao", "x" * 4001),
    ],
)
def test_preparar_recusa_titulo_e_descricao_fora_do_tamanho(campo, valor):
    _, erro = _preparar(_estado(), **{campo: valor})
    assert erro is not None and {"titulo": "título", "descricao": "descrição"}[campo] in erro


def test_preparar_aceita_titulo_e_descricao_no_limite():
    assert _preparar(_estado(), titulo="x" * 120, descricao="y" * 4000)[1] is None


# --- abrir: proposta + confirmação explícita (a matriz inteira) -------------


def _abrir(estado: dict, **args):
    return aplicar_politica("abrir_chamado", args, estado)


def test_abrir_aceita_proposta_valida_com_sim_no_turno_seguinte():
    proposta = _proposta_pronta(1)
    mensagens = [
        HumanMessage(content="1"),
        AIMessage(content="Posso abrir?"),
        HumanMessage(content="sim"),
    ]
    args, erro = _abrir(_estado(proposta_chamado=proposta, messages=mensagens))
    assert erro is None and args["proposta"] == proposta


def test_abrir_sem_proposta_e_recusado():
    _, erro = _abrir(_estado(messages=[HumanMessage(content="sim")]))
    assert erro is not None and "preparar_abertura_chamado" in erro


@pytest.mark.parametrize("mensagem", ["esse", "Esse", "isso", "esse mesmo", "48213", "não", "oi"])
def test_abrir_recusado_quando_a_mensagem_nao_e_confirmacao_explicita(mensagem):
    mensagens = [HumanMessage(content="1"), HumanMessage(content=mensagem)]
    _, erro = _abrir(_estado(proposta_chamado=_proposta_pronta(1), messages=mensagens))
    assert erro is not None and "confirmação explícita" in erro


def test_abrir_recusado_na_mesma_rodada_do_preparo():
    msgs = [HumanMessage(content="abre um chamado pra mim")]
    proposta = carimbar_proposta({"titulo": "x", "categoria": "sem_conexao"}, {"messages": msgs})
    _, erro = _abrir(_estado(proposta_chamado=proposta, messages=msgs))
    assert erro is not None and "NESTE turno" in erro


def test_abrir_recusado_quando_a_proposta_venceu():
    velha = carimbar_proposta(
        {"titulo": "x", "categoria": "sem_conexao"},
        {"messages": [HumanMessage(content="1")]},
        agora=datetime.now(UTC) - PROPOSTA_VALIDADE - timedelta(seconds=1),
    )
    mensagens = [HumanMessage(content="1"), HumanMessage(content="sim")]
    _, erro = _abrir(_estado(proposta_chamado=velha, messages=mensagens))
    assert erro is not None and "venceu" in erro


def test_abrir_recusado_quando_a_conversa_seguiu_com_proposta_real():
    msgs = [HumanMessage(content="1"), HumanMessage(content="2"), HumanMessage(content="sim")]
    _, erro = _abrir(_estado(proposta_chamado=_proposta_pronta(1), messages=msgs))
    assert erro is not None and "SEGUINTE" in erro


def test_abrir_ignora_a_proposta_que_o_llm_tentar_passar():
    proposta = _proposta_pronta(1)
    mensagens = [HumanMessage(content="1"), HumanMessage(content="pode abrir")]
    args, erro = _abrir(
        _estado(proposta_chamado=proposta, messages=mensagens),
        proposta={"titulo": "OUTRA COISA", "categoria": "financeiro"},
    )
    assert erro is None and args["proposta"] == proposta


def test_confirmacoes_da_sofia_incluem_sim_sofia_pode_abrir_e_confirmo():
    for frase in ("sim sofia", "pode abrir", "confirmo"):
        assert frase in tool_policy.CONFIRMACOES_EXPLICITAS
    assert not {"esse", "isso", "essa", "este", "aquele"} & tool_policy.CONFIRMACOES_EXPLICITAS


# --- consultar_status_chamado: número só dígitos ----------------------------


@pytest.mark.parametrize("numero", ["48213", "1", "1234567890", " 48213 ", "#48213"])
def test_consultar_aceita_numero_so_digitos(numero):
    assert aplicar_politica("consultar_status_chamado", {"numero": numero}, _estado())[1] is None


@pytest.mark.parametrize(
    "numero", ["", "abc", "48a13", "12345678901", "-1", "4821.5", "48213; DROP"]
)
def test_consultar_recusa_numero_invalido(numero):
    _, erro = aplicar_politica("consultar_status_chamado", {"numero": numero}, _estado())
    assert erro is not None and "número de chamado inválido" in erro


# --- transferir_para_atendente: motivo do enum ------------------------------


@pytest.mark.parametrize("motivo", ["pedido_cliente", "fora_do_escopo", "nao_resolvido"])
def test_transferir_aceita_os_motivos_do_enum(motivo):
    assert aplicar_politica("transferir_para_atendente", {"motivo": motivo}, _estado())[1] is None


@pytest.mark.parametrize("motivo", ["frustracao", "midia", "qualquer coisa", "", None])
def test_transferir_recusa_motivo_fora_do_enum(motivo):
    _, erro = aplicar_politica("transferir_para_atendente", {"motivo": motivo}, _estado())
    assert erro is not None and "motivo inválido" in erro


def test_listar_nao_tem_args_nem_precondicao():
    estado = _estado(contato=None, classificacao=None)
    args, erro = aplicar_politica("listar_meus_chamados", {}, estado)
    assert erro is None and set(args) == {"telefone", "conversation_id"}


# ===========================================================================
# E2.3 — efeitos no estado (carimbar / consumir / gravar identificação)
# ===========================================================================


def test_efeitos_registrados_sao_os_das_quatro_tools_com_estado():
    assert set(efeitos.EFEITOS) == {
        "informar_identificacao",
        "preparar_abertura_chamado",
        "abrir_chamado",
        "cadastrar_solicitante",
    }


def test_preparar_ok_carimba_a_proposta_com_turno_e_validade():
    resultado = {"status": True, "proposta": {"titulo": "t", "categoria": "sem_conexao"}}
    estado = _estado()
    delta = efeitos.efeito_pos_tool("preparar_abertura_chamado", resultado, estado)
    proposta = delta["proposta_chamado"]
    assert proposta["titulo"] == "t" and proposta["turno"] == 1
    assert proposta["criada_em"].endswith("+00:00")
    assert estado.get("proposta_chamado") is None  # não muta o estado de entrada


@pytest.mark.parametrize(
    "resultado",
    [
        {"status": False, "erro": "x", "motivo": "chamado_aberto"},
        {"status": True},  # ok sem proposta
        {"erro": "a ferramenta falhou agora"},
        "texto",
        None,
    ],
)
def test_preparar_que_nao_deu_certo_zera_a_proposta_anterior(resultado):
    delta = efeitos.efeito_pos_tool(
        "preparar_abertura_chamado", resultado, _estado(proposta_chamado=_proposta_pronta())
    )
    assert delta == {"proposta_chamado": None}


def test_abrir_ok_consome_a_proposta():
    delta = efeitos.efeito_pos_tool("abrir_chamado", {"status": True, "numero": "1"}, _estado())
    assert delta == {"proposta_chamado": None}


@pytest.mark.parametrize("motivo", ["em_andamento", "infodesk_indisponivel", "escopo_insuficiente"])
def test_abrir_com_falha_tambem_consome_a_proposta(motivo):
    """Nunca um segundo `abrir` com a mesma proposta (regra 3): o modelo teria de
    preparar de novo, e o `preparar` do gateway devolve `chamado_aberto` se o ticket
    existir."""
    delta = efeitos.efeito_pos_tool(
        "abrir_chamado", {"status": False, "erro": "e", "motivo": motivo}, _estado()
    )
    assert delta == {"proposta_chamado": None}


def test_segundo_abrir_no_mesmo_turno_depois_de_falha_e_recusado_pela_politica():
    proposta = _proposta_pronta(1)
    mensagens = [HumanMessage(content="1"), HumanMessage(content="sim")]
    estado = _estado(proposta_chamado=proposta, messages=mensagens)
    assert aplicar_politica("abrir_chamado", {}, estado)[1] is None
    delta = efeitos.efeito_pos_tool(
        "abrir_chamado", {"status": False, "erro": "e", "motivo": "infodesk_indisponivel"}, estado
    )
    depois = {**estado, **delta}
    _, erro = aplicar_politica("abrir_chamado", {}, depois)
    assert erro is not None and "preparar_abertura_chamado" in erro


def _estado_com_chamada_informar(**args) -> dict:
    chamada = {"id": "t1", "name": "informar_identificacao", "args": args}
    ai = AIMessage(content="", tool_calls=[chamada])
    return _estado(messages=[HumanMessage(content="meu cpf é 123.456.789-09"), ai])


def test_informar_ok_grava_so_forma_e_valor_mascarado():
    estado = _estado_com_chamada_informar(cpf="123.456.789-09")
    resultado = {
        "status": True,
        "identificado": True,
        "forma": "cpf",
        "cliente_id": None,
        "nome_cadastro": None,
    }
    delta = efeitos.efeito_pos_tool("informar_identificacao", resultado, estado)
    assert delta == {"identificacao": {"forma": "cpf", "valor_mascarado": "***.***.789-**"}}
    assert "12345678909" not in str(delta) and "123.456.789-09" not in str(delta)


def test_informar_ok_por_contrato_grava_o_mascarado():
    estado = _estado_com_chamada_informar(contrato="000123456")
    resultado = {"status": True, "identificado": True, "forma": "contrato"}
    delta = efeitos.efeito_pos_tool("informar_identificacao", resultado, estado)
    assert delta == {"identificacao": {"forma": "contrato", "valor_mascarado": "******456"}}


@pytest.mark.parametrize(
    "resultado",
    [
        {"status": False, "erro": "e", "motivo": "campo_invalido"},
        {"status": True, "identificado": False, "forma": "cpf"},
        {"status": True, "identificado": True, "forma": "telefone"},
    ],
)
def test_informar_que_nao_identificou_nao_grava_nada(resultado):
    delta = efeitos.efeito_pos_tool(
        "informar_identificacao", resultado, _estado_com_chamada_informar(cpf="12345678909")
    )
    assert delta == {}


def test_informar_sem_a_chamada_no_historico_grava_mascara_total_nunca_o_dado():
    resultado = {"status": True, "identificado": True, "forma": "cpf"}
    delta = efeitos.efeito_pos_tool("informar_identificacao", resultado, _estado())
    assert delta == {"identificacao": {"forma": "cpf", "valor_mascarado": "***.***.***-**"}}
