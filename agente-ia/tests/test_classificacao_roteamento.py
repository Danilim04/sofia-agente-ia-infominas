"""E3.10 — categoria também fora de reclamação, para o roteamento do handoff.

Boleto e preço (intenções `outro`/`duvida_informacao`) passam a chegar à
transferência com a categoria fixada (financeiro/comercial na taxonomia
proposta), não `indefinida`. Abrir chamado continua dependendo da política das
tools; a pergunta de esclarecimento continua só em reclamação. Sem rede.
"""

from __future__ import annotations

import asyncio

import pytest

from src.agent.tool_policy import aplicar_politica
from src.classificacao.config import ConfigClassificacao
from src.classificacao.fallback_llm import ResultadoFallback
from src.classificacao.roteador import classificar
from src.classificacao.taxonomia import carregar_taxonomia
from src.classificacao.tipos import INTENCOES_COM_CATEGORIA, Classificacao, Mensagem
from tests.fakes import FakeFallbackLLM, FakeJEV, config_de_teste, resp_jev

PROPOSTA = ConfigClassificacao(_env_file=None).taxonomia_path.with_name(  # type: ignore[call-arg]
    "taxonomia.proposta.yaml"
)
TAX = carregar_taxonomia(PROPOSTA)


def _rodar(
    texto: str,
    jev: FakeJEV | None,
    *,
    fallback: FakeFallbackLLM | None = None,
    anterior: Classificacao | None = None,
    **config: object,
) -> Classificacao:
    return asyncio.run(
        classificar(
            [Mensagem("cliente", texto)],
            anterior,
            jev,
            fallback,
            taxonomia=TAX,
            config=config_de_teste(**config),
        )
    )


@pytest.mark.parametrize(
    ("texto", "intencao", "categoria"),
    [
        ("preciso do boleto desse mês", "outro", "financeiro"),
        ("pode mandar a segunda via da fatura?", "duvida_informacao", "financeiro"),
        ("quanto custa o plano de 500 mega?", "duvida_informacao", "comercial"),
        ("quero contratar mais um ponto", "outro", "comercial"),
    ],
)
def test_pedido_de_informacao_fixa_a_categoria_sem_esclarecimento(texto, intencao, categoria):
    jev = FakeJEV(padrao=resp_jev(intencao=(intencao, 0.85), categoria=(categoria, 0.9)))
    c = _rodar(texto, jev)
    assert (c.categoria, c.origem) == (categoria, "jev")
    assert c.precisa_esclarecer is False  # quem só pediu informação não é indagado
    assert c.acoes == ()  # nada de transferência por baixa confiança


def test_duvida_com_baixa_confianca_fica_indefinida_sem_perguntar():
    jev = FakeJEV(
        padrao=resp_jev(intencao=("duvida_informacao", 0.9), categoria=("comercial", 0.2))
    )
    c = _rodar("tem como me ajudar?", jev)
    assert (c.categoria, c.origem) == ("indefinida", "indefinida")
    assert c.precisa_esclarecer is False and c.acoes == ()


def test_reclamacao_segue_igual_pergunta_quando_incerta():
    jev = FakeJEV(padrao=resp_jev(categoria=("internet_rede", 0.2)))
    c = _rodar("tá tudo estranho aqui", jev)
    assert (c.categoria, c.origem, c.precisa_esclarecer) == ("indefinida", "esclarecimento", True)


def test_categoria_do_fallback_llm_tambem_vale_fora_de_reclamacao():
    fb = FakeFallbackLLM(padrao=ResultadoFallback("outro", "financeiro"))
    jev = FakeJEV(
        padrao=resp_jev(intencao=("outro", 0.9), categoria=("financeiro", 0.5, "comercial"))
    )
    c = _rodar("manda o boleto", jev, fallback=fb)
    assert (c.categoria, c.origem) == ("financeiro", "llm")


def test_jev_fora_o_fallback_ainda_fixa_a_categoria_do_pedido():
    fb = FakeFallbackLLM(padrao=ResultadoFallback("outro", "comercial"))
    c = _rodar("quero saber de planos", FakeJEV(padrao=RuntimeError("503")), fallback=fb)
    assert (c.categoria, c.origem, c.jev_disponivel) == ("comercial", "llm", False)


# ---------------------------------------------------------------------------
# Conjunto configurável (JEV_INTENCOES_COM_CATEGORIA)
# ---------------------------------------------------------------------------


def test_default_e_reclamacao_outro_e_duvida():
    assert config_de_teste().intencoes_com_categoria == INTENCOES_COM_CATEGORIA
    assert INTENCOES_COM_CATEGORIA == {"reclamacao_problema", "outro", "duvida_informacao"}


def test_conjunto_configurado_so_reclamacao_volta_ao_comportamento_do_e2_2():
    jev = FakeJEV(padrao=resp_jev(intencao=("outro", 0.9), categoria=("financeiro", 0.9)))
    c = _rodar("manda o boleto", jev, JEV_INTENCOES_COM_CATEGORIA="reclamacao_problema")
    assert c.categoria == "indefinida"


def test_conjunto_configurado_pode_incluir_status_chamado():
    jev = FakeJEV(padrao=resp_jev(intencao=("status_chamado", 0.9), categoria=("financeiro", 0.9)))
    c = _rodar(
        "e o meu boleto?", jev, JEV_INTENCOES_COM_CATEGORIA=" reclamacao_problema , status_chamado "
    )
    assert c.categoria == "financeiro"


@pytest.mark.parametrize("valor", ["", "reclamacao_problema,inventada", " , "])
def test_conjunto_invalido_e_recusado(valor):
    with pytest.raises(ValueError, match="JEV_INTENCOES_COM_CATEGORIA"):
        config_de_teste(JEV_INTENCOES_COM_CATEGORIA=valor)


def test_conjunto_vem_do_ambiente(monkeypatch):
    monkeypatch.setenv("JEV_INTENCOES_COM_CATEGORIA", "reclamacao_problema,outro")
    cfg = ConfigClassificacao(_env_file=None)  # type: ignore[call-arg]
    assert cfg.intencoes_com_categoria == {"reclamacao_problema", "outro"}


# ---------------------------------------------------------------------------
# Pegajosidade (mesma regra; informação não trava a fila da reclamação)
# ---------------------------------------------------------------------------


def test_categoria_de_informacao_e_substituida_por_nova_informacao():
    c1 = _rodar(
        "manda o boleto",
        FakeJEV(padrao=resp_jev(intencao=("outro", 0.9), categoria=("financeiro", 0.9))),
    )
    c2 = _rodar(
        "e quanto custa o plano?",
        FakeJEV(padrao=resp_jev(intencao=("duvida_informacao", 0.9), categoria=("comercial", 0.8))),
        anterior=c1,
    )
    assert c2.categoria == "comercial"


def test_reclamacao_aberta_nao_e_roubada_por_pedido_de_informacao():
    c1 = _rodar("meu link caiu", FakeJEV(padrao=resp_jev(categoria=("internet_rede", 0.9))))
    c2 = _rodar(
        "aliás, manda o boleto",
        FakeJEV(padrao=resp_jev(intencao=("outro", 0.9), categoria=("financeiro", 0.8))),
        anterior=c1,
    )
    assert c2.categoria == "internet_rede"


# ---------------------------------------------------------------------------
# A transferência leva a categoria fixada
# ---------------------------------------------------------------------------


def test_transferir_para_atendente_recebe_a_categoria_fixada_do_pedido_de_boleto():
    c = _rodar(
        "preciso do boleto desse mês",
        FakeJEV(padrao=resp_jev(intencao=("outro", 0.85), categoria=("financeiro", 0.9))),
    )
    estado = {
        "classificacao": c.para_estado(),
        "conversation_id": "c-1",
        "telefone": "+5531999990156",
    }
    args, recusa = aplicar_politica(
        "transferir_para_atendente", {"motivo": "fora_do_escopo"}, estado
    )
    assert recusa is None
    # a política injeta a classificação da SESSÃO (nunca um valor do LLM)
    assert args["classificacao"]["categoria"] == "financeiro"


def test_pedido_de_atendente_depois_do_boleto_mantem_a_categoria_na_acao():
    c1 = _rodar(
        "preciso do boleto",
        FakeJEV(padrao=resp_jev(intencao=("outro", 0.85), categoria=("financeiro", 0.9))),
    )
    c2 = _rodar(
        "quero falar com uma pessoa",
        FakeJEV(padrao=resp_jev(intencao=("pedir_atendente", 0.9), categoria=("indefinida", 0.9))),
        anterior=c1,
    )
    assert [a.origem for a in c2.acoes] == ["pedido_cliente"]
    assert c2.categoria == "financeiro"  # herdada: o handoff vai ao departamento certo


# ---------------------------------------------------------------------------
# Revisão E3.10: "categoria do chamado" (reclamação) × "categoria do pedido" (informação)
# ---------------------------------------------------------------------------


def _fixa_informacao(categoria: str = "financeiro") -> Classificacao:
    return _rodar(
        "manda o boleto",
        FakeJEV(padrao=resp_jev(intencao=("outro", 0.9), categoria=(categoria, 0.9))),
    )


def _fixa_reclamacao(categoria: str = "internet_rede") -> Classificacao:
    return _rodar("meu link caiu", FakeJEV(padrao=resp_jev(categoria=(categoria, 0.9))))


def test_reclamacao_incerta_nao_herda_a_categoria_de_um_pedido_de_informacao():  # repro A
    c1 = _fixa_informacao()
    c2 = _rodar(
        "minha internet caiu",
        FakeJEV(padrao=resp_jev(categoria=("internet_rede", 0.2))),
        anterior=c1,
    )
    assert (c2.categoria, c2.origem, c2.precisa_esclarecer) == (
        "indefinida",
        "esclarecimento",
        True,
    )


def test_reclamacao_com_jev_fora_nao_herda_a_categoria_de_um_pedido_de_informacao():  # repro B
    c1 = _fixa_informacao()
    fb = FakeFallbackLLM(padrao=ResultadoFallback("reclamacao_problema", "indefinida"))
    c2 = _rodar(
        "minha internet caiu", FakeJEV(padrao=RuntimeError("503")), fallback=fb, anterior=c1
    )
    assert (c2.categoria, c2.origem, c2.precisa_esclarecer) == (
        "indefinida",
        "esclarecimento",
        True,
    )


def test_jev_e_fallback_fora_nao_alteram_a_classificacao_anterior():
    # Sem JEV nem fallback a intenção é desconhecida ("outro"): nada a reclassificar,
    # o episódio segue com o que já estava fixado (não inventa categoria nem pergunta).
    c1 = _fixa_informacao()
    c2 = _rodar("minha internet caiu", FakeJEV(padrao=RuntimeError("503")), anterior=c1)
    assert c2.categoria == "financeiro" and c2.precisa_esclarecer is False


@pytest.mark.parametrize("intencao", ["outro", "duvida_informacao"])
def test_informacao_nunca_troca_a_fila_de_uma_reclamacao_aberta(intencao):  # repro C
    c1 = _fixa_reclamacao()
    jev = FakeJEV(padrao=resp_jev(intencao=(intencao, 0.95), categoria=("financeiro", 0.99)))
    c2 = _rodar("e o boleto?", jev, anterior=c1)
    assert c2.categoria == "internet_rede"
    # nem com "correção explícita": quem corrige a fila é a própria reclamação
    c3 = _rodar("na verdade o problema é o boleto", jev, anterior=c1)
    assert c3.categoria == "internet_rede"
    # e um "ok" depois também não devolve nada ao pedido de informação
    ok = FakeJEV(padrao=resp_jev(intencao=("confirmacao", 0.95), categoria=("indefinida", 0.9)))
    assert _rodar("ok, valeu", ok, anterior=c2).categoria == "internet_rede"


def test_categoria_informativa_continua_nao_travando_depois_de_um_ok():  # repro D
    c1 = _fixa_informacao()
    ok = FakeJEV(padrao=resp_jev(intencao=("confirmacao", 0.95), categoria=("indefinida", 0.9)))
    c2 = _rodar("ok", ok, anterior=c1)
    assert c2.categoria == "financeiro"
    # o "ok" preservou a intenção que fixou a categoria (vai no estado, 5 chaves do contrato)
    assert c2.para_estado()["intencao"] == "outro"
    assert set(c2.para_estado()) == {"intencao", "categoria", "prioridade", "confianca", "origem"}
    anterior = Classificacao.de_estado(c2.para_estado())
    c3 = _rodar(
        "agora minha internet caiu",
        FakeJEV(padrao=resp_jev(categoria=("internet_rede", 0.7))),
        anterior=anterior,
    )
    assert (c3.categoria, c3.origem) == ("internet_rede", "jev")  # 0.7 < 0.85 e ainda assim troca


def test_estado_sem_intencao_gravada_e_tratado_como_reclamacao_pegajosa():
    anterior = Classificacao.de_estado(
        {"categoria": "internet_rede", "prioridade": "alta", "confianca": 0.9, "origem": "jev"}
    )
    jev = FakeJEV(padrao=resp_jev(intencao=("outro", 0.9), categoria=("financeiro", 0.99)))
    assert _rodar("e o boleto?", jev, anterior=anterior).categoria == "internet_rede"


def test_prioridade_da_reclamacao_aberta_nao_cai_com_pedido_de_informacao_mesmo_sem_a_flag():
    c1 = _rodar(
        "tudo parado aqui",
        FakeJEV(padrao=resp_jev(categoria=("internet_rede", 0.9), prioridade=(3, 0.9))),
    )
    assert c1.prioridade == "urgente"
    jev = FakeJEV(
        padrao=resp_jev(intencao=("outro", 0.9), categoria=("financeiro", 0.9), prioridade=(0, 0.9))
    )
    c2 = _rodar("e quanto custa o plano?", jev, anterior=c1, JEV_PRIORIDADE_SO_SOBE=False)
    assert (c2.categoria, c2.prioridade) == ("internet_rede", "urgente")


def test_conjunto_invalido_no_ambiente_falha_ao_carregar_a_config(monkeypatch):
    monkeypatch.setenv("JEV_INTENCOES_COM_CATEGORIA", "reclamacao_problema,inventada")
    with pytest.raises(ValueError, match="JEV_INTENCOES_COM_CATEGORIA"):
        ConfigClassificacao(_env_file=None)  # type: ignore[call-arg]
