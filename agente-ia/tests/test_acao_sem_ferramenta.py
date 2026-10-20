"""Testes de prevenção — resposta que AFIRMA ação sem ferramenta no turno (ZE-2).

Incidente herdado: o agente respondeu "já
deixei o recado registrado" sem ter chamado ferramenta nenhuma. Fora de um
chamado aberto por ferramenta não existe recado, anotação nem aviso ao time.

Regra determinística do `output_guardrail_node` (o LLM nunca faz parte da base
confiável): resposta final que afirma registro/aviso/encaminhamento/abertura
num turno em que NENHUMA ferramenta executou com sucesso é trocada por
`RESPOSTA_ACAO_SEM_FERRAMENTA` (oferece abrir chamado). Falsos positivos
cobertos: frase no passado dita pelo cliente (citada ou em 3ª pessoa),
negação ("ainda não registrei"), promessa futura ("vou registrar") e turno em
que a ferramenta de chamado foi de fato executada.
"""

from __future__ import annotations

import json
import logging
from datetime import UTC, datetime
from unittest.mock import MagicMock

import pytest
from langchain_core.messages import AIMessage, HumanMessage, ToolMessage

from src.agent import nodes
from src.agent.graph import build_graph
from src.agent.prompts import RESPOSTA_ACAO_SEM_FERRAMENTA, RESPOSTA_ERRO_INTERNO
from src.security.output_guardrails import afirmacoes_de_acao
from tests.fakes import registrar_duas_fases_de_teste, tool_abrir_de_teste

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_SUCESSO_ABERTURA = {
    "status": True,
    "numero": "4821",
    "mesa": "Suporte",
    "transferido": True,
}

_CONTATO = {
    "forma": "cpf",
    "valor_mascarado": "***.***.789-**",
    "cliente_id": None,
    "nome_cadastro": None,
}


def _tool_msg(nome: str, payload: dict, tool_call_id: str = "tc1") -> ToolMessage:
    return ToolMessage(
        content=json.dumps(payload, ensure_ascii=False),
        tool_call_id=tool_call_id,
        name=nome,
    )


def _ai_tool_call(nome: str, args: dict | None = None, tool_call_id: str = "tc1") -> AIMessage:
    return AIMessage(
        content="", tool_calls=[{"id": tool_call_id, "name": nome, "args": args or {}}]
    )


def _guardrail(messages: list) -> dict:
    return nodes.output_guardrail_node({"messages": messages, "fontes_usadas": []})


def _fake_llm(scripted: list[AIMessage]) -> MagicMock:
    fake = MagicMock(name="FakeLLM")
    fake.bind_tools.return_value = fake
    fake.invoke.side_effect = list(scripted)
    return fake


def _passa_seguranca(monkeypatch) -> None:
    monkeypatch.setattr(
        nodes,
        "avaliar_entrada",
        lambda texto, **_: {"is_safe": True, "categoria": "suporte", "motivo": "test"},
    )


def _sem_rag(pergunta: str) -> dict:
    return {"encontrado": False, "total": 0, "chunks": []}


# ---------------------------------------------------------------------------
# (1) Turno SEM tool call cuja resposta afirma registro/aviso → barrado
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "resposta",
    [
        # O incidente, literalmente.
        "Já deixei o recado registrado para o time, Daniel. Assim que tiverem novidade, te retornam.",
        "Registrei aqui sua solicitação e o time vai analisar.",
        "Anotei aqui e já avisei o time técnico sobre a instabilidade.",
        "Encaminhei sua solicitação para o setor responsável.",
        "Pronto! Abri o chamado para você, pode ficar tranquilo.",
        "Já está registrado do nosso lado, ok?",
        "Estou acionando a equipe técnica agora mesmo.",
        "Seu recado já foi repassado ao time.",
        "Deixei um aviso para o pessoal do financeiro.",
        "Já passei o caso pro time de desenvolvimento.",
        "Ja deixei registrado aqui, sem acento mesmo.",  # normalização de acento
        "Certinho então, o chamado já foi aberto e o time vai te chamar.",
    ],
)
def test_afirmacao_de_acao_sem_ferramenta_e_barrada(resposta):
    ultima = AIMessage(content=resposta)
    out = _guardrail([HumanMessage(content="minha internet caiu, resolve pra mim"), ultima])

    novo = out["messages"][0]
    assert novo.content == RESPOSTA_ACAO_SEM_FERRAMENTA
    assert novo.id == ultima.id  # add_messages substitui, não duplica


def test_barrar_emite_log_de_aviso_sem_expor_a_resposta_no_info(caplog):
    with caplog.at_level(logging.INFO, logger="src.agent.nodes"):
        _guardrail(
            [
                HumanMessage(content="x"),
                AIMessage(content="Já deixei o recado registrado. Pode ficar tranquilo, Daniel."),
            ]
        )
    avisos = [r for r in caplog.records if "output_guardrail_acao_sem_ferramenta" in r.getMessage()]
    assert avisos and avisos[0].levelno == logging.WARNING
    # O aviso traz só os trechos casados; a resposta inteira vai para DEBUG.
    assert "recado registrado" in avisos[0].getMessage()
    assert not any(
        "ficar tranquilo" in r.getMessage() for r in caplog.records if r.levelno >= logging.INFO
    )


# ---------------------------------------------------------------------------
# (2) Turno com a tool de chamado chamada COM SUCESSO → passa intacto
# ---------------------------------------------------------------------------


def test_turno_com_tool_de_chamado_bem_sucedida_passa():
    ultima = AIMessage(
        content=(
            "Pronto! Abri o chamado 4821 para você. A equipe de Suporte assume a partir daqui."
        )
    )
    out = _guardrail(
        [
            HumanMessage(content="pode abrir"),
            _ai_tool_call("abrir_chamado"),
            _tool_msg("abrir_chamado", _SUCESSO_ABERTURA),
            ultima,
        ]
    )
    assert out == {}  # nada a corrigir — a afirmação tem ferramenta por trás


def test_turno_com_consulta_bem_sucedida_tambem_passa():
    """Qualquer ferramenta executada com sucesso no turno ancora a resposta."""
    out = _guardrail(
        [
            HumanMessage(content="tenho chamados abertos?"),
            _ai_tool_call("listar_meus_chamados"),
            _tool_msg("listar_meus_chamados", {"status": True, "total": 0, "chamados": []}),
            AIMessage(content="Verifiquei aqui e já registrei: você não tem chamados abertos."),
        ]
    )
    assert out == {}


# ---------------------------------------------------------------------------
# (3) Resposta comum sem afirmação de ação → passa
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "resposta",
    [
        "Para reiniciar, desligue o roteador da tomada por 30 segundos e ligue de novo. Se não voltar, me avisa.",
        "Quer que eu abra um chamado para o time verificar?",
        "Vou registrar 'sem conexão desde cedo'. Posso abrir?",
        "Verifiquei aqui e não encontrei isso na base. Posso abrir um chamado para o time confirmar?",
        "Você pode registrar a reclamação pelo app da Infominas também, se preferir.",
        "Eu que agradeço! Qualquer coisa, é só chamar. 😊",
        "Posso preparar o chamado com esse resumo: sem conexão desde cedo. Pode abrir?",
        "Você tem algum chamado aberto sobre isso? Posso listar para você.",
        "Depois de aberto, o chamado fica registrado no sistema e você acompanha pelo número.",
        "Como te informei acima, o número do chamado vem na mensagem de confirmação.",
        "Boa tarde, Maria! Sou a Sofia, da Infominas. Como posso te ajudar? 😊",
    ],
)
def test_resposta_comum_sem_afirmacao_passa(resposta):
    out = _guardrail([HumanMessage(content="oi"), AIMessage(content=resposta)])
    assert out == {}


# ---------------------------------------------------------------------------
# Falsos positivos que o filtro precisa tolerar
# ---------------------------------------------------------------------------


def test_frase_no_passado_dita_pelo_cliente_e_citada_nao_barra():
    resposta = (
        'Entendi, você comentou "já registrei a reclamação no app" e ela não aparece. '
        "Consegue me dizer em qual tela você olhou?"
    )
    assert (
        _guardrail([HumanMessage(content="já registrei e sumiu"), AIMessage(content=resposta)])
        == {}
    )


def test_acao_do_cliente_em_terceira_pessoa_nao_barra():
    resposta = (
        "Certinho: você registrou a reclamação e o técnico anotou a visita. "
        "Vamos ver por que não consta — me passa o número do chamado?"
    )
    assert (
        _guardrail([HumanMessage(content="registrei a reclamação"), AIMessage(content=resposta)])
        == {}
    )


@pytest.mark.parametrize(
    "resposta",
    [
        "Ainda não registrei nada do nosso lado — posso abrir um chamado?",
        "Eu não abri o chamado ainda, Daniel. Confirma o resumo para eu abrir?",
        "Não anotei nenhuma solicitação até agora; quer que eu prepare um chamado?",
    ],
)
def test_negacao_de_acao_nao_barra(resposta):
    assert _guardrail([HumanMessage(content="e aí?"), AIMessage(content=resposta)]) == {}


def test_tool_de_turno_anterior_nao_conta():
    """A ferramenta precisa ter rodado NESTE turno (depois da última humana)."""
    ultima = AIMessage(content="Já registrei esse também, pode deixar.")
    out = _guardrail(
        [
            HumanMessage(content="abre um chamado: sem conexão"),
            _ai_tool_call("abrir_chamado"),
            _tool_msg("abrir_chamado", _SUCESSO_ABERTURA),
            AIMessage(content="Pronto! Abri o chamado 4821."),
            HumanMessage(content="e a fatura que não chegou?"),
            ultima,
        ]
    )
    assert out["messages"][0].content == RESPOSTA_ACAO_SEM_FERRAMENTA


def test_tool_recusada_pela_politica_nao_conta_como_acao():
    out = _guardrail(
        [
            HumanMessage(content="pode abrir"),
            _ai_tool_call("abrir_chamado"),
            _tool_msg(
                "abrir_chamado",
                {"erro": "não há proposta de chamado preparada", "recusado_pela_politica": True},
            ),
            AIMessage(content="Pronto! Abri o chamado para você."),
        ]
    )
    assert out["messages"][0].content == RESPOSTA_ACAO_SEM_FERRAMENTA


def test_tool_que_falhou_nao_conta_como_acao():
    out = _guardrail(
        [
            HumanMessage(content="pode abrir"),
            _ai_tool_call("abrir_chamado"),
            _tool_msg(
                "abrir_chamado",
                {"status": False, "erro": "o Infodesk está indisponível agora"},
            ),
            AIMessage(content="Registrei aqui e abro assim que o sistema voltar, ok?"),
        ]
    )
    assert out["messages"][0].content == RESPOSTA_ACAO_SEM_FERRAMENTA


def test_vazamento_interno_tem_precedencia_sobre_afirmacao():
    ultima = AIMessage(content='Registrei aqui. Traceback (most recent call last) File "/app/x.py"')
    out = _guardrail([HumanMessage(content="x"), ultima])
    assert out["messages"][0].content == RESPOSTA_ERRO_INTERNO


# ---------------------------------------------------------------------------
# Unidades: detector textual e "houve ferramenta no turno?"
# ---------------------------------------------------------------------------


def test_afirmacoes_de_acao_devolve_trechos_e_ignora_acento_e_caixa():
    achadas = afirmacoes_de_acao("Já DEIXEI o recado registrado e avisei o time.")
    assert achadas
    assert any("deixei" in a for a in achadas)
    assert afirmacoes_de_acao("Quer que eu abra um chamado?") == []
    assert afirmacoes_de_acao("") == []
    assert afirmacoes_de_acao(None) == []


def test_turno_executou_ferramenta_distingue_sucesso_falha_e_turno():
    sucesso = _tool_msg("listar_meus_chamados", {"status": True, "total": 0, "chamados": []})
    falha = _tool_msg("listar_meus_chamados", {"status": False, "erro": "indisponível"})
    recusa = _tool_msg("abrir_chamado", {"erro": "x", "recusado_pela_politica": True})
    desconhecida = _tool_msg("xpto", {"erro": "tool desconhecida: xpto"})
    excecao = _tool_msg("xpto", {"erro": "a ferramenta falhou agora; tente de novo"})
    h, tc = HumanMessage(content="a"), _ai_tool_call("x")

    assert nodes._turno_executou_ferramenta([h, tc, sucesso]) is True
    assert nodes._turno_executou_ferramenta([h, tc, falha]) is False
    assert nodes._turno_executou_ferramenta([h, tc, recusa]) is False
    assert nodes._turno_executou_ferramenta([h, tc, desconhecida]) is False
    assert nodes._turno_executou_ferramenta([h, tc, excecao]) is False
    # uma falha seguida de um sucesso no mesmo turno conta
    assert nodes._turno_executou_ferramenta([h, tc, falha, tc, sucesso]) is True
    # ToolMessage de turno ANTERIOR não conta
    assert (
        nodes._turno_executou_ferramenta(
            [h, tc, sucesso, AIMessage(content="ok"), HumanMessage(content="b")]
        )
        is False
    )
    assert nodes._turno_executou_ferramenta([]) is False
    assert nodes._turno_executou_ferramenta(None) is False
    # resultado que não é JSON (texto/RAG legado) é execução válida
    rag = ToolMessage(
        content="<documento_externo>...</documento_externo>",
        tool_call_id="t",
        name="consultar_base_conhecimento",
    )
    assert nodes._turno_executou_ferramenta([h, tc, rag]) is True
    # consulta que não achou nada (sem erro) ainda é execução
    vazio = _tool_msg("consultar_status_chamado", {"numero": "4821", "encontrado": False})
    assert nodes._turno_executou_ferramenta([h, tc, vazio]) is True


# ---------------------------------------------------------------------------
# Resposta segura — propriedades
# ---------------------------------------------------------------------------


def test_resposta_segura_e_whatsapp_native_e_abre_fluxo_de_chamado():
    r = RESPOSTA_ACAO_SEM_FERRAMENTA
    assert len(r) <= 400
    assert "---" not in r and "#" not in r and "http" not in r
    assert "chamado" in r.lower()
    # Termina perguntando → o próximo turno é fluxo ativo (classificador pulado)
    assert r.rstrip().endswith("?")
    assert nodes._fluxo_ativo(
        [HumanMessage(content="a"), AIMessage(content=r), HumanMessage(content="sim")]
    )
    # A própria resposta segura não dispara o filtro (sem loop).
    assert afirmacoes_de_acao(r) == []
    # Não afirma nada feito: nada de "registrei"/"abri" fora de negação.
    assert "deixei" not in r.lower()


# ---------------------------------------------------------------------------
# E2E no grafo real (LLM fake, sem rede)
# ---------------------------------------------------------------------------


def test_e2e_grafo_troca_afirmacao_sem_ferramenta_pela_resposta_segura(monkeypatch):
    _passa_seguranca(monkeypatch)
    fake = _fake_llm([AIMessage(content="Já deixei o recado registrado para o time, Daniel!")])
    g = build_graph(llm=fake, tools=[], buscar_chunks=_sem_rag)

    out = g.invoke(
        {
            "telefone": "11999990001",
            "contato": _CONTATO,
            "messages": [HumanMessage(content="minha internet caiu")],
        },
        config={"configurable": {"thread_id": "ze2-sem-tool"}},
    )

    assert out["messages"][-1].content == RESPOSTA_ACAO_SEM_FERRAMENTA
    # A resposta original não sobrevive no estado (reducer substitui pelo id).
    assert not any(
        "recado registrado" in str(m.content) for m in out["messages"] if isinstance(m, AIMessage)
    )


def test_e2e_grafo_abertura_real_de_chamado_mantem_resposta(monkeypatch):
    """Fluxo real: proposta carimbada no estado → tool de abertura (política e
    efeito de teste, sem rede) → resposta que narra a abertura passa intacta."""
    _passa_seguranca(monkeypatch)
    registrar_duas_fases_de_teste(monkeypatch)

    final = "Pronto! Abri o chamado 4821. A equipe de Suporte assume a partir daqui."
    fake = _fake_llm([_ai_tool_call("tool_abrir_de_teste"), AIMessage(content=final)])
    g = build_graph(llm=fake, tools=[tool_abrir_de_teste], buscar_chunks=_sem_rag)

    out = g.invoke(
        {
            "telefone": "5531999990000",
            "contato": _CONTATO,
            # Proposta carimbada: preparada no turno anterior, dentro da
            # validade — "pode abrir" neste turno é a confirmação explícita.
            "proposta_chamado": {
                "titulo": "SEM CONEXÃO",
                "categoria": "sem_conexao",
                "criada_em": datetime.now(UTC).isoformat(),
                "turno": 0,
            },
            "messages": [HumanMessage(content="pode abrir")],
        },
        config={"configurable": {"thread_id": "ze2-com-tool"}},
    )

    tool_msgs = [m for m in out["messages"] if isinstance(m, ToolMessage)]
    assert len(tool_msgs) == 1 and '"status": true' in tool_msgs[0].content
    assert out["messages"][-1].content == final
    assert out["proposta_chamado"] is None  # proposta consumida pela abertura
