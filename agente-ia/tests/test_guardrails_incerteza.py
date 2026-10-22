"""Guardrail de saída (E3.8): incerteza honesta não é afirmação de ação.

Depois de um timeout em `abrir_chamado` a Sofia diz "não consegui confirmar SE o
chamado foi registrado" — isso não afirma ação nenhuma e não pode ser trocado por
`RESPOSTA_ACAO_SEM_FERRAMENTA`. Ao mesmo tempo, o "se" só vale na mesma oração:
afirmar a ação e condicionar outra coisa continua barrado.
"""

from __future__ import annotations

import pytest
from langchain_core.messages import AIMessage, HumanMessage

from src.agent import nodes
from src.agent.prompts import RESPOSTA_ACAO_SEM_FERRAMENTA
from src.security.output_guardrails import afirmacoes_de_acao


@pytest.mark.parametrize(
    "frase",
    [
        "Não consegui confirmar se o chamado foi registrado, então não vou abrir outro. "
        "Um atendente vai conferir.",
        "Não sei se o chamado foi registrado; um atendente vai conferir.",
        "Não tenho certeza se registrei direito o seu caso.",
        "Vou verificar se o recado foi anotado.",
        "Não consegui ver se o time foi avisado, mas o atendente confere.",
        "Sem saber se o ticket foi criado, prefiro não abrir outro.",
        "Preciso confirmar se o chamado já foi registrado antes de seguir.",
        "Se o chamado já foi aberto, o atendente te avisa.",
    ],
)
def test_incerteza_e_condicional_nao_contam_como_afirmacao(frase):
    assert afirmacoes_de_acao(frase) == [], frase


@pytest.mark.parametrize(
    "frase",
    [
        # já barradas antes da E3.8 — têm de continuar barradas
        "Registrei o seu caso e avisei o time.",
        "Já abri o chamado para você.",
        "Pronto! Chamado criado com sucesso.",
        "O seu chamado foi registrado.",
        "Deixei o recado registrado.",
        "Estou encaminhando o seu caso agora.",
        # o "se" fica em OUTRA oração: a ação foi afirmada
        "Abri o chamado, se precisar de algo é só chamar.",
        "Se quiser, abri o chamado.",
        "Registrei o seu caso. Se precisar, me chame.",
        "Já avisei o time, mas não sei se vão responder hoje.",
        "Anotei tudo e, se tiver dúvida, me avise.",
    ],
)
def test_afirmacao_de_acao_continua_barrada(frase):
    assert afirmacoes_de_acao(frase) != [], frase


def test_resposta_incerta_depois_de_falha_da_tool_sobrevive_ao_guardrail():
    texto = (
        "Não consegui confirmar se o chamado foi registrado, então não vou abrir outro. "
        "Um atendente vai conferir e assumir."
    )
    out = nodes.output_guardrail_node(
        {"messages": [HumanMessage(content="sim"), AIMessage(content=texto)], "fontes_usadas": []}
    )
    assert out == {}  # nada trocado


def test_afirmacao_sem_ferramenta_ainda_e_trocada_pela_resposta_segura():
    out = nodes.output_guardrail_node(
        {
            "messages": [
                HumanMessage(content="sim"),
                AIMessage(content="Pronto, abri o chamado para você."),
            ],
            "fontes_usadas": [],
        }
    )
    assert out["messages"][0].content == RESPOSTA_ACAO_SEM_FERRAMENTA
