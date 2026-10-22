"""Roteador da classificação (E2.2): cada linha da tabela de limiares do
`docs/plano/02-classificacao.md`, fail-soft, pegajosidade e PII — tudo com
`FakeJEV`/`FakeFallbackLLM` (sem rede).
"""

from __future__ import annotations

import asyncio

import pytest

from src.classificacao.fallback_llm import ResultadoFallback
from src.classificacao.jev import JanelaDisponibilidade, JEVIndisponivel
from src.classificacao.roteador import classificar, corrigiu_explicitamente
from src.classificacao.tipos import Classificacao, Mensagem
from tests.fakes import (
    FakeFallbackLLM,
    FakeJEV,
    config_de_teste,
    resp_jev,
    taxonomia_de_teste,
)

TAX = taxonomia_de_teste()


def _rodar(
    texto: str = "minha internet caiu",
    *,
    jev: FakeJEV | None,
    fallback: FakeFallbackLLM | None = None,
    anterior: Classificacao | None = None,
    esclarecimentos: int = 0,
    historico: tuple[Mensagem, ...] = (),
    nomes: tuple[str, ...] = (),
    disponibilidade: JanelaDisponibilidade | None = None,
    **config: object,
) -> Classificacao:
    return asyncio.run(
        classificar(
            [*historico, Mensagem("cliente", texto)],
            anterior,
            jev,
            fallback,
            taxonomia=TAX,
            config=config_de_teste(**config),
            esclarecimentos_feitos=esclarecimentos,
            nomes=nomes,
            disponibilidade=disponibilidade,
        )
    )


def _fixa(categoria: str = "sem_conexao", prioridade: str = "alta", origem: str = "jev"):
    return Classificacao.de_estado(
        {
            "intencao": "reclamacao_problema",
            "categoria": categoria,
            "prioridade": prioridade,
            "confianca": 0.9,
            "origem": origem,
        }
    )


# ===========================================================================
# Intenção: ≥ 0.70 usa · 0.45–0.70 usa e confirma · < 0.45 fallback LLM
# ===========================================================================


def test_intencao_alta_usa_o_jev_sem_fallback():
    fb = FakeFallbackLLM()
    jev = FakeJEV(padrao=resp_jev(intencao=("duvida_informacao", 0.71)))
    c = _rodar(jev=jev, fallback=fb)
    assert c.intencao == "duvida_informacao"
    assert c.origens["intencao"] == "jev"
    assert c.confirmar_intencao is False
    assert fb.chamadas == []  # nenhuma chamada de LLM no caminho feliz
    assert len(jev.chamadas) == 1  # UMA chamada ao JEV com todas as perguntas


def test_intencao_entre_baixo_e_alto_usa_e_manda_confirmar():
    fb = FakeFallbackLLM()
    c = _rodar(jev=FakeJEV(padrao=resp_jev(intencao=("status_chamado", 0.55))), fallback=fb)
    assert c.intencao == "status_chamado"
    assert c.confirmar_intencao is True
    assert fb.chamadas == []


def test_intencao_baixa_cai_no_fallback_llm():
    fb = FakeFallbackLLM(padrao=ResultadoFallback("reclamacao_problema", "lentidao"))
    c = _rodar(jev=FakeJEV(padrao=resp_jev(intencao=("outro", 0.30))), fallback=fb)
    assert c.intencao == "reclamacao_problema"
    assert c.origens["intencao"] == "llm"
    assert len(fb.chamadas) == 1


def test_intencao_baixa_sem_fallback_mantem_jev_e_manda_confirmar():
    c = _rodar(jev=FakeJEV(padrao=resp_jev(intencao=("outro", 0.30))), fallback=None)
    assert c.intencao == "outro"
    assert c.confirmar_intencao is True


def test_limiares_vem_de_config_e_nao_do_codigo():
    jev = FakeJEV(padrao=resp_jev(intencao=("status_chamado", 0.55)))
    c = _rodar(jev=jev, JEV_LIMIAR_INTENCAO_ALTO=0.5)
    assert c.confirmar_intencao is False  # 0.55 agora é "alto"


# ===========================================================================
# Categoria: ≥ 0.65 fixa · 0.40–0.65 fallback concorda → llm / senão pergunta ·
# < 0.40 pergunta (máx. 1) · persistindo → indefinida (+ transferir)
# ===========================================================================


def test_categoria_alta_fixa_com_origem_jev():
    fb = FakeFallbackLLM()
    c = _rodar(jev=FakeJEV(padrao=resp_jev(categoria=("sem_conexao", 0.65))), fallback=fb)
    assert (c.categoria, c.origem) == ("sem_conexao", "jev")
    assert c.confianca == pytest.approx(0.65)
    assert fb.chamadas == []
    assert c.precisa_esclarecer is False


@pytest.mark.parametrize("concorda_com", ["lentidao", "instabilidade"])
def test_categoria_media_fallback_concorda_com_1a_ou_2a_fixa_como_llm(concorda_com):
    # JEV: 1ª = lentidao, 2ª = instabilidade, confiança no meio (0.40–0.65).
    jev = FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.5, "instabilidade")))
    fb = FakeFallbackLLM(padrao=ResultadoFallback("reclamacao_problema", concorda_com))
    c = _rodar(jev=jev, fallback=fb)
    assert (c.categoria, c.origem) == (concorda_com, "llm")
    assert c.precisa_esclarecer is False


def test_categoria_media_fallback_discorda_vira_esclarecimento():
    jev = FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.5, "instabilidade")))
    fb = FakeFallbackLLM(padrao=ResultadoFallback("reclamacao_problema", "cobranca"))
    c = _rodar(jev=jev, fallback=fb)
    assert c.categoria == "indefinida"
    assert c.origem == "esclarecimento"
    assert c.precisa_esclarecer is True
    assert set(c.candidatas) == {"lentidao", "instabilidade"}


def test_categoria_baixa_pergunta_sem_chamar_o_fallback():
    fb = FakeFallbackLLM()
    jev = FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.39, "instabilidade")))
    c = _rodar(jev=jev, fallback=fb)
    assert (c.categoria, c.origem, c.precisa_esclarecer) == ("indefinida", "esclarecimento", True)
    assert fb.chamadas == []


def test_categoria_persistindo_indefinida_apos_esclarecimento_transfere_por_confianca():
    jev = FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.2)))
    c = _rodar(jev=jev, esclarecimentos=1)  # já perguntou 1 vez (máx. 1 por episódio)
    assert (c.categoria, c.origem, c.precisa_esclarecer) == ("indefinida", "indefinida", False)
    assert [a.como_dict() for a in c.acoes] == [
        {
            "tipo": "transferir",
            "origem": "confianca",
            "motivo": "categoria indefinida após pergunta de esclarecimento",
        }
    ]


def test_categoria_indefinida_pode_ir_para_triagem_em_vez_de_transferir():
    # TODO(P11): decisão da Infominas — hipótese configurável.
    jev = FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.2)))
    c = _rodar(jev=jev, esclarecimentos=1, JEV_ACAO_INDEFINIDA="triagem")
    assert c.categoria == "indefinida"
    assert c.acoes == ()


def test_max_esclarecimentos_e_configuravel():
    jev = FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.2)))
    c = _rodar(jev=jev, esclarecimentos=1, JEV_MAX_ESCLARECIMENTOS=2)
    assert c.precisa_esclarecer is True


def test_categoria_incerta_fora_de_reclamacao_nao_pergunta_nem_transfere():
    jev = FakeJEV(
        padrao=resp_jev(intencao=("duvida_informacao", 0.9), categoria=("duvida_planos", 0.2))
    )
    c = _rodar("como funciona o plano?", jev=jev)
    assert c.categoria == "indefinida"
    assert c.precisa_esclarecer is False
    assert c.acoes == ()


def test_jev_devolvendo_categoria_fora_da_taxonomia_e_tratada_como_ausente():
    jev = FakeJEV(padrao=resp_jev(categoria=("inventada", 0.99)))
    c = _rodar(jev=jev)
    assert c.categoria == "indefinida"
    assert c.precisa_esclarecer is True


# ===========================================================================
# Prioridade: conf ≥ 0.60 usa o score · senão `media` · piso por categoria vence
# ===========================================================================


def test_prioridade_com_confianca_usa_o_score_do_jev():
    c = _rodar(jev=FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.9), prioridade=(3, 0.6))))
    assert c.prioridade == "urgente"
    assert c.origens["prioridade"] == "jev"


def test_prioridade_sem_confianca_vira_media():
    c = _rodar(jev=FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.9), prioridade=(3, 0.59))))
    assert c.prioridade == "media"
    assert c.origens["prioridade"] == "padrao"


def test_piso_da_categoria_vence_o_score_do_jev():
    # `sem_conexao` tem prioridade_minima: alta (taxonomia) — o JEV disse "baixa".
    c = _rodar(jev=FakeJEV(padrao=resp_jev(categoria=("sem_conexao", 0.9), prioridade=(0, 0.95))))
    assert c.prioridade == "alta"
    assert c.origens["prioridade"] == "regra"


def test_piso_da_categoria_vale_tambem_sem_confianca_na_prioridade():
    c = _rodar(jev=FakeJEV(padrao=resp_jev(categoria=("sem_conexao", 0.9), prioridade=(0, 0.1))))
    assert c.prioridade == "alta"


def test_nivel_de_prioridade_fora_da_faixa_e_limitado():
    c = _rodar(jev=FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.9), prioridade=(9, 0.9))))
    assert c.prioridade == "urgente"


# ===========================================================================
# Frustração: ≥ 3 e conf ≥ 0.6 → transferir · ≥ 2 → tom cuidadoso
# ===========================================================================


def test_frustracao_maxima_com_confianca_transfere():
    c = _rodar(jev=FakeJEV(padrao=resp_jev(frustracao=(3, 0.6))))
    assert [a.origem for a in c.acoes] == ["frustracao"]
    assert c.acoes[0].tipo == "transferir"
    assert c.tom_cuidadoso is False


def test_frustracao_maxima_sem_confianca_nao_transfere():
    c = _rodar(jev=FakeJEV(padrao=resp_jev(frustracao=(3, 0.59))))
    assert c.acoes == ()
    assert c.frustracao == 3


def test_frustracao_nivel_2_so_pede_tom_cuidadoso():
    c = _rodar(jev=FakeJEV(padrao=resp_jev(frustracao=(2, 0.9))))
    assert c.acoes == ()
    assert c.tom_cuidadoso is True


def test_frustracao_nivel_1_nao_faz_nada():
    c = _rodar(jev=FakeJEV(padrao=resp_jev(frustracao=(1, 0.9))))
    assert c.acoes == ()
    assert c.tom_cuidadoso is False


# ===========================================================================
# Pedir atendente: JEV com conf ≥ 0.6 → transferir(pedido_cliente)
# ===========================================================================


def test_pedir_atendente_com_confianca_transfere():
    jev = FakeJEV(padrao=resp_jev(intencao=("pedir_atendente", 0.6)))
    # 0.60 fica abaixo do "alto" da intenção (0.70): usa e manda confirmar, mas já transfere.
    c = _rodar("quero falar com uma pessoa", jev=jev)
    assert [a.origem for a in c.acoes] == ["pedido_cliente"]


def test_pedir_atendente_entre_baixo_e_limiar_nao_transfere():
    jev = FakeJEV(padrao=resp_jev(intencao=("pedir_atendente", 0.5)))
    c = _rodar("atendente", jev=jev)
    assert c.intencao == "pedir_atendente"
    assert c.acoes == ()


def test_pedir_atendente_decidido_so_pelo_llm_nao_dispara_acao():
    # A redundância determinística é a regex do gateway; o LLM não tem confiança calibrada.
    fb = FakeFallbackLLM(padrao=ResultadoFallback("pedir_atendente", "indefinida"))
    c = _rodar("atendente", jev=FakeJEV(padrao=resp_jev(intencao=("outro", 0.2))), fallback=fb)
    assert c.intencao == "pedir_atendente"
    assert c.acoes == ()


def test_pedido_de_atendente_vence_a_frustracao_e_so_ha_uma_transferencia():
    jev = FakeJEV(padrao=resp_jev(intencao=("pedir_atendente", 0.9), frustracao=(3, 0.9)))
    c = _rodar("chega, quero um atendente", jev=jev)
    assert [a.origem for a in c.acoes] == ["pedido_cliente"]


# ===========================================================================
# Segurança: os Nouls passam crus (o nó decide bloquear/avisar com os limiares)
# ===========================================================================


def test_nouls_de_seguranca_sao_repassados():
    c = _rodar(jev=FakeJEV(padrao=resp_jev(malicioso=0.83, fora_de_escopo=0.4)))
    assert c.malicioso == pytest.approx(0.83)
    assert c.fora_de_escopo == pytest.approx(0.4)


# ===========================================================================
# Fail-soft: JEV fora → fallback LLM → pergunta de esclarecimento
# ===========================================================================


def test_jev_fora_cai_no_fallback_llm_para_intencao_e_categoria():
    fb = FakeFallbackLLM(padrao=ResultadoFallback("reclamacao_problema", "lentidao"))
    jev = FakeJEV(padrao=JEVIndisponivel("timeout"))
    c = _rodar(jev=jev, fallback=fb)
    assert c.jev_disponivel is False
    assert (c.intencao, c.categoria, c.origem) == ("reclamacao_problema", "lentidao", "llm")
    assert c.origens["intencao"] == "llm"
    assert c.acoes == ()  # sem frustração/malicioso: sem JEV, sinais neutros


def test_jev_fora_e_fallback_indefinido_vira_pergunta_de_esclarecimento():
    fb = FakeFallbackLLM(padrao=ResultadoFallback("reclamacao_problema", "indefinida"))
    c = _rodar(jev=FakeJEV(padrao=RuntimeError("503")), fallback=fb)
    assert (c.categoria, c.origem, c.precisa_esclarecer) == ("indefinida", "esclarecimento", True)


def test_jev_fora_e_fallback_fora_nao_derruba_o_turno():
    fb = FakeFallbackLLM(padrao=ResultadoFallback("outro", "indefinida", ok=False))
    c = _rodar(jev=FakeJEV(padrao=RuntimeError("503")), fallback=fb)
    assert (c.intencao, c.categoria, c.origem) == ("outro", "indefinida", "indefinida")
    assert (
        c.precisa_esclarecer is False
    )  # intenção desconhecida: não inventa pergunta de reclamação


def test_fallback_que_levanta_excecao_tambem_e_absorvido():
    fb = FakeFallbackLLM([RuntimeError("openrouter 402")])
    c = _rodar(jev=FakeJEV(padrao=RuntimeError("503")), fallback=fb)
    assert c.categoria == "indefinida"


def test_sem_jev_configurado_usa_so_o_fallback():
    fb = FakeFallbackLLM(padrao=ResultadoFallback("reclamacao_problema", "sem_conexao"))
    c = _rodar(jev=None, fallback=fb)
    assert (c.categoria, c.origem, c.jev_disponivel) == ("sem_conexao", "llm", False)


def test_fallback_sem_corroboracao_do_jev_pode_ser_desligado():
    fb = FakeFallbackLLM(padrao=ResultadoFallback("reclamacao_problema", "sem_conexao"))
    c = _rodar(jev=None, fallback=fb, JEV_FALLBACK_FIXA_SEM_JEV=False)
    assert c.categoria == "indefinida"
    assert c.precisa_esclarecer is True


def test_disponibilidade_do_jev_vira_alerta_acima_de_5_por_cento(caplog):
    janela = JanelaDisponibilidade(janela_s=900, limite=0.05)
    fb = FakeFallbackLLM(padrao=ResultadoFallback("outro", "indefinida"))
    with caplog.at_level("WARNING"):
        for _ in range(20):
            _rodar(jev=FakeJEV(padrao=RuntimeError("503")), fallback=fb, disponibilidade=janela)
    assert janela.taxa_indisponivel == 1.0
    assert "jev_indisponivel_alerta" in caplog.text
    assert "jev_indisponivel erro=" in caplog.text


# ===========================================================================
# Pegajosidade
# ===========================================================================


def test_confirmacao_herda_categoria_e_prioridade_do_episodio():
    anterior = _fixa("sem_conexao", "urgente")
    # Em "sim" o JEV nem acerta categoria — não pode apagar a classificação.
    jev = FakeJEV(
        padrao=resp_jev(
            intencao=("confirmacao", 0.95), categoria=("indefinida", 0.9), prioridade=(0, 0.9)
        )
    )
    c = _rodar("sim, pode abrir", jev=jev, anterior=anterior)
    assert (c.categoria, c.prioridade, c.origem) == ("sem_conexao", "urgente", "jev")
    assert c.origens["categoria"] == "herdada"


def test_categoria_fixada_nao_troca_com_confianca_abaixo_de_085():
    anterior = _fixa("sem_conexao")
    jev = FakeJEV(padrao=resp_jev(categoria=("cobranca", 0.80)))
    c = _rodar("e a fatura veio errada também", jev=jev, anterior=anterior)
    assert c.categoria == "sem_conexao"
    assert c.origens["categoria"] == "herdada"


def test_categoria_fixada_troca_com_confianca_a_partir_de_085():
    anterior = _fixa("sem_conexao")
    jev = FakeJEV(padrao=resp_jev(categoria=("cobranca", 0.85)))
    c = _rodar("a cobrança veio errada", jev=jev, anterior=anterior)
    assert (c.categoria, c.origem) == ("cobranca", "jev")


def test_correcao_explicita_do_cliente_troca_a_categoria():
    anterior = _fixa("sem_conexao")
    jev = FakeJEV(padrao=resp_jev(categoria=("cobranca", 0.70)))
    c = _rodar("na verdade o problema é a cobrança", jev=jev, anterior=anterior)
    assert c.categoria == "cobranca"


def test_nova_classificacao_incerta_nao_apaga_a_categoria_fixada():
    anterior = _fixa("sem_conexao")
    jev = FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.2)))
    c = _rodar("hmm", jev=jev, anterior=anterior)
    assert c.categoria == "sem_conexao"
    assert c.precisa_esclarecer is False


def test_categoria_do_esclarecimento_nao_e_pegajosa():
    anterior = Classificacao.de_estado(
        {
            "categoria": "indefinida",
            "prioridade": "media",
            "confianca": 0.2,
            "origem": "esclarecimento",
        }
    )
    jev = FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.7)))
    c = _rodar("a net tá lenta", jev=jev, anterior=anterior, esclarecimentos=1)
    assert (c.categoria, c.origem) == ("lentidao", "jev")


def test_prioridade_so_sobe_dentro_da_mesma_categoria():
    anterior = _fixa("lentidao", "urgente")
    jev = FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.9), prioridade=(0, 0.9)))
    c = _rodar("tá, obrigado", jev=jev, anterior=anterior)
    assert c.prioridade == "urgente"


@pytest.mark.parametrize(
    ("texto", "esperado"),
    [
        ("Na verdade é a fatura", True),
        ("não é isso, é outra coisa", True),
        ("me enganei, desculpa", True),
        ("minha internet caiu", False),
        ("na verdade nem sei", False),
        ("na verdade, o assunto é outro", True),
        ("", False),
    ],
)
def test_corrigiu_explicitamente(texto, esperado):
    assert corrigiu_explicitamente(texto) is esperado


# ===========================================================================
# PII: o estado enviado ao JEV sai mascarado
# ===========================================================================


def test_estado_enviado_ao_jev_nao_tem_cpf_telefone_nem_nome():
    jev = FakeJEV(padrao=resp_jev())
    historico = (
        Mensagem("sofia", "Oi Maria! Pode me passar o CPF?"),
        Mensagem("cliente", "meu cpf é 123.456.789-01 e meu telefone (31) 99999-0001"),
    )
    _rodar("minha internet caiu, sou a Maria Souza", jev=jev, historico=historico, nomes=("Maria",))
    estado = jev.chamadas[0][0]
    assert "123.456.789-01" not in estado
    assert "99999-0001" not in estado
    assert "Maria" not in estado
    assert "<CPF>" in estado and "<TEL>" in estado and "<NOME>" in estado
    assert estado.endswith("Mensagem atual: minha internet caiu, sou a <NOME>")


# ===========================================================================
# Fábrica de produção: JEV real só com chave (P8); fallback sempre
# ===========================================================================


def test_servico_padrao_sem_chave_nao_cria_jev_e_avisa(caplog):
    from src.classificacao.fallback_llm import FallbackLLMEstruturado
    from src.classificacao.roteador import criar_servico_padrao

    with caplog.at_level("WARNING"):
        servico = criar_servico_padrao(lambda: None, config_de_teste(), TAX)
    assert servico.jev is None
    assert isinstance(servico.fallback, FallbackLLMEstruturado)
    assert "jev_sem_chave" in caplog.text


def test_servico_padrao_com_chave_cria_o_cliente_real_com_modelo_pinado():
    from src.classificacao.jev import JEVTypeSafe
    from src.classificacao.roteador import criar_servico_padrao

    servico = criar_servico_padrao(
        lambda: None, config_de_teste(TYPESAFE_API_KEY="chave-de-teste"), TAX
    )
    assert isinstance(servico.jev, JEVTypeSafe)
    assert servico.config.jev_model == "jev-1.13.0"


# ===========================================================================
# Revisão E2.2 / E3.10: pegajosidade entre reclamação e informação · bordas · flag
# ===========================================================================


def test_duvida_de_cobranca_nao_trava_a_fila_da_reclamacao_seguinte():
    # E3.10: a dúvida fixa a categoria (serve ao handoff), mas NÃO a torna pegajosa
    # contra a reclamação que vier depois — é ela que define a fila do chamado.
    duvida = FakeJEV(
        padrao=resp_jev(intencao=("duvida_informacao", 0.9), categoria=("cobranca", 0.9))
    )
    c1 = _rodar("como pego a 2ª via do boleto?", jev=duvida)
    assert (c1.categoria, c1.origem) == ("cobranca", "jev")
    assert c1.precisa_esclarecer is False and c1.acoes == ()

    reclamacao = FakeJEV(padrao=resp_jev(categoria=("sem_conexao", 0.8)))
    c2 = _rodar("aliás minha internet caiu", jev=reclamacao, anterior=c1)
    assert (c2.categoria, c2.origem) == ("sem_conexao", "jev")


def test_pedido_de_informacao_nao_rouba_a_fila_de_uma_reclamacao_aberta():
    anterior = _fixa("sem_conexao")  # fixada numa RECLAMAÇÃO (pegajosa)
    jev = FakeJEV(
        padrao=resp_jev(intencao=("duvida_informacao", 0.95), categoria=("cobranca", 0.8))
    )
    assert _rodar("e quanto custa o boleto?", jev=jev, anterior=anterior).categoria == "sem_conexao"


def test_duvida_com_categoria_incerta_segue_os_mesmos_limiares_do_02():
    # 0.40–0.65: consulta o fallback; < 0.40: nem consulta — e nunca pergunta (só reclamação pergunta)
    fb = FakeFallbackLLM(padrao=ResultadoFallback("duvida_informacao", "cobranca"))
    meio = FakeJEV(
        padrao=resp_jev(
            intencao=("duvida_informacao", 0.9), categoria=("cobranca", 0.5, "segunda_via")
        )
    )
    c = _rodar("quanto custa o plano?", jev=meio, fallback=fb)
    assert len(fb.chamadas) == 1 and (c.categoria, c.origem) == ("cobranca", "llm")

    fb = FakeFallbackLLM()
    baixo = FakeJEV(
        padrao=resp_jev(intencao=("duvida_informacao", 0.9), categoria=("cobranca", 0.3))
    )
    c = _rodar("quanto custa o plano?", jev=baixo, fallback=fb)
    assert fb.chamadas == []
    assert c.categoria == "indefinida" and c.precisa_esclarecer is False and c.acoes == ()


@pytest.mark.parametrize("intencao", ["status_chamado", "saudacao_social", "confirmacao"])
def test_intencoes_fora_do_conjunto_herdam_a_categoria_do_episodio(intencao):
    anterior = _fixa("sem_conexao")
    jev = FakeJEV(padrao=resp_jev(intencao=(intencao, 0.95), categoria=("cobranca", 0.99)))
    assert _rodar("x", jev=jev, anterior=anterior).categoria == "sem_conexao"


def test_bordas_exatas_dos_limiares_de_intencao_e_categoria():
    # intenção: 0.70 é "alto" (não confirma); 0.45 ainda usa o JEV (e confirma)
    c = _rodar(jev=FakeJEV(padrao=resp_jev(intencao=("status_chamado", 0.70))))
    assert c.confirmar_intencao is False and c.origens["intencao"] == "jev"
    fb = FakeFallbackLLM()
    c = _rodar(jev=FakeJEV(padrao=resp_jev(intencao=("status_chamado", 0.45))), fallback=fb)
    assert c.confirmar_intencao is True and fb.chamadas == []
    # categoria: 0.40 já é "entre" (consulta o fallback); 0.65 fixa direto
    fb = FakeFallbackLLM(padrao=ResultadoFallback("reclamacao_problema", "lentidao"))
    c = _rodar(
        jev=FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.40, "instabilidade"))), fallback=fb
    )
    assert len(fb.chamadas) == 1 and c.origem == "llm"


def test_prioridade_so_sobe_pode_ser_desligada():
    anterior = _fixa("lentidao", "urgente")
    jev = FakeJEV(padrao=resp_jev(categoria=("lentidao", 0.9), prioridade=(0, 0.9)))
    c = _rodar("tá, obrigado", jev=jev, anterior=anterior, JEV_PRIORIDADE_SO_SOBE=False)
    assert c.prioridade == "baixa"
