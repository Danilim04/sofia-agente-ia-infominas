"""Roteador da classificação — CÓDIGO, não modelo (docs/plano/02, "Roteador").

Recebe as respostas do JEV (e, quando a confiança não basta, do fallback LLM) e
decide: intenção, categoria (pegajosa no episódio), prioridade (com piso por
categoria), frustração, segurança e as `acoes` do turno. Os limiares vêm de
`config.py` (`JEV_LIMIAR_*`), com os defaults do 02.

Tabela implementada (cada linha tem teste em `tests/test_classificacao_roteador.py`):

| Sinal | Regra |
|---|---|
| intenção | conf ≥ alto → usa; baixo ≤ conf < alto → usa e `confirmar_intencao`; < baixo → fallback LLM |
| categoria (só em reclamação) | conf ≥ alto → fixa (`jev`); baixo ≤ conf < alto → fallback LLM: concorda com a 1ª/2ª do JEV → fixa (`llm`), senão esclarecimento; < baixo → esclarecimento (máx. 1/episódio); persistindo → `indefinida` (+ `transferir(confianca)` ou triagem, P11) |
| prioridade | conf ≥ limiar → score do JEV; senão default da categoria/`media`; piso `prioridade_minima` da taxonomia vence |
| frustração | nível ≥ 3 e conf ≥ 0.6 → `transferir(frustracao)`; nível ≥ 2 → tom cuidadoso |
| malicioso | ≥ 0.80 bloqueia; 0.50–0.80 aviso no prompt (decidido no nó `classificar`) |
| fora de escopo | ≥ 0.80 → dica `off_topic` (não bloqueia) |
| pedir atendente | JEV, conf ≥ 0.6 → `transferir(pedido_cliente)` |
"""

from __future__ import annotations

import logging
import re
import time
import unicodedata
from collections.abc import Callable, Sequence
from dataclasses import dataclass, field
from typing import Any

from src.classificacao.config import ConfigClassificacao, get_config_classificacao
from src.classificacao.fallback_llm import FallbackLLM, FallbackLLMEstruturado, ResultadoFallback
from src.classificacao.jev import (
    ClienteJEV,
    JanelaDisponibilidade,
    JEVTypeSafe,
    estado_para_jev,
)
from src.classificacao.perguntas import (
    CATEGORIA,
    FORA_DE_ESCOPO,
    FRUSTRACAO,
    INTENCAO,
    MALICIOSO,
    PRIORIDADE,
    montar_perguntas,
)
from src.classificacao.taxonomia import Taxonomia, carregar_taxonomia
from src.classificacao.tipos import (
    CATEGORIA_INDEFINIDA,
    INTENCAO_OUTRO,
    INTENCAO_PEDIR_ATENDENTE,
    INTENCAO_RECLAMACAO,
    INTENCOES,
    PRIORIDADE_PADRAO,
    PRIORIDADES,
    Acao,
    Classificacao,
    Mensagem,
    Origem,
    OrigemPergunta,
    RespostaJEV,
    ordem_prioridade,
)

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# Correção explícita do cliente (única forma, além de conf ≥ troca, de trocar
# uma categoria já fixada). TODO(P8): calibrar com o histórico real (P9).
# ---------------------------------------------------------------------------

_CORRECOES = re.compile(
    r"\b(na verdade,? (?:e|era|foi|o problema|o assunto|se trata|quero|queria)\b|"
    r"nao e isso|nao e esse|nao e essa|nao eh isso|me enganei|errei|"
    r"me expressei mal|corrigindo|o problema e outro|nao,? (?:o problema|e sobre|e a respeito))"
)


def _sem_acento(texto: str) -> str:
    base = unicodedata.normalize("NFKD", texto.lower())
    return "".join(c for c in base if not unicodedata.combining(c))


def corrigiu_explicitamente(texto: str) -> bool:
    """O cliente está corrigindo o que disse antes ("na verdade é a cobrança")?"""
    return bool(_CORRECOES.search(_sem_acento(texto)))


# ---------------------------------------------------------------------------
# Núcleo
# ---------------------------------------------------------------------------


def _ultima_do_cliente(mensagens: Sequence[Mensagem]) -> str:
    for m in reversed(mensagens):
        if m.autor == "cliente":
            return m.texto
    return ""


def _limitar(valor: int, minimo: int, maximo: int) -> int:
    return max(minimo, min(maximo, valor))


@dataclass
class _DecisaoCategoria:
    categoria: str
    confianca: float
    origem: Origem
    origem_pergunta: OrigemPergunta
    candidatas: tuple[str, ...] = ()
    precisa_esclarecer: bool = False


async def classificar(
    mensagens: Sequence[Mensagem],
    anterior: Classificacao | None,
    jev: ClienteJEV | None,
    fallback: FallbackLLM | None,
    *,
    taxonomia: Taxonomia,
    config: ConfigClassificacao | None = None,
    esclarecimentos_feitos: int = 0,
    nomes: Sequence[str] = (),
    disponibilidade: JanelaDisponibilidade | None = None,
) -> Classificacao:
    """Classifica o turno atual (a última mensagem do cliente em `mensagens`).

    `anterior`: classificação já fixada no episódio (pegajosidade).
    `esclarecimentos_feitos`: perguntas de esclarecimento já feitas no episódio.
    Nunca levanta por falha de JEV/LLM: degrada para o fallback e, no limite,
    para `outro`/`indefinida`.
    """
    cfg = config or get_config_classificacao()
    inicio = time.perf_counter()
    estado = estado_para_jev(
        mensagens, janela=cfg.jev_janela_msgs, max_tokens=cfg.jev_max_tokens_estado, nomes=nomes
    )
    texto_atual = _ultima_do_cliente(mensagens)

    # --- 1. uma chamada ao JEV com todas as perguntas -----------------------
    resposta: RespostaJEV | None = None
    if jev is not None:
        try:
            resposta = await jev.perguntar(estado, montar_perguntas(taxonomia))
        except Exception as exc:
            # Métrica `jev_indisponivel` (contador por log estruturado) — fail-soft.
            logger.warning("jev_indisponivel erro=%s", type(exc).__name__)
    jev_ok = resposta is not None
    if disponibilidade is not None and disponibilidade.registrar(jev_ok):
        logger.error(
            "jev_indisponivel_alerta taxa=%.2f janela_s=%.0f — acima de %.0f%% dos turnos",
            disponibilidade.taxa_indisponivel,
            cfg.jev_alerta_janela_s,
            cfg.jev_alerta_indisponivel * 100,
        )

    # --- 2. fallback LLM, no máximo uma vez por turno e só se preciso --------
    resultado_fb: ResultadoFallback | None = None
    fb_tentado = False

    async def fallback_llm() -> ResultadoFallback | None:
        nonlocal resultado_fb, fb_tentado
        if not fb_tentado:
            fb_tentado = True
            if fallback is not None:
                try:
                    resultado_fb = await fallback.classificar(estado)
                except Exception as exc:
                    logger.warning("fallback_llm_erro erro=%s", type(exc).__name__)
                    resultado_fb = None
        if resultado_fb is not None and resultado_fb.ok:
            return resultado_fb
        return None

    # --- 3. intenção ---------------------------------------------------------
    r_int = resposta.choices.get(INTENCAO) if resposta else None
    confirmar_intencao = False
    if r_int is not None and r_int.confianca >= cfg.limiar_intencao_baixo:
        intencao, conf_int, origem_int = r_int.escolha, r_int.confianca, "jev"
        confirmar_intencao = conf_int < cfg.limiar_intencao_alto
    else:
        fb = await fallback_llm()
        if fb is not None:
            intencao, conf_int, origem_int = fb.intencao, 0.0, "llm"
        elif r_int is not None:
            # Sem fallback: mantém a leitura do JEV, mas manda o prompt confirmar.
            intencao, conf_int, origem_int = r_int.escolha, r_int.confianca, "jev"
            confirmar_intencao = True
        else:
            intencao, conf_int, origem_int = INTENCAO_OUTRO, 0.0, "indefinida"
    if intencao not in INTENCOES:
        intencao = INTENCAO_OUTRO
    origem_int_t: OrigemPergunta = origem_int  # type: ignore[assignment]

    # --- 4. categoria --------------------------------------------------------
    if intencao in cfg.intencoes_com_categoria:
        decisao = await _decidir_categoria(
            resposta=resposta,
            fallback_llm=fallback_llm,
            taxonomia=taxonomia,
            cfg=cfg,
            anterior=anterior,
            intencao=intencao,
            texto_atual=texto_atual,
            esclarecimentos_feitos=esclarecimentos_feitos,
        )
    elif anterior is not None:
        decisao = _DecisaoCategoria(
            anterior.categoria, anterior.confianca, anterior.origem, "herdada"
        )
    else:
        decisao = _DecisaoCategoria(CATEGORIA_INDEFINIDA, 0.0, "indefinida", "indefinida")

    # --- 5. prioridade -------------------------------------------------------
    prioridade, conf_prio, origem_prio = _decidir_prioridade(
        resposta=resposta,
        taxonomia=taxonomia,
        cfg=cfg,
        categoria=decisao.categoria,
        anterior=anterior,
        # Categoria mantida de uma reclamação aberta por um pedido de informação: a
        # prioridade do chamado também (um "quanto custa?" não rebaixa um urgente).
        herdar=intencao not in cfg.intencoes_com_categoria
        or (decisao.origem_pergunta == "herdada" and intencao != INTENCAO_RECLAMACAO),
    )

    # --- 6. frustração e segurança ------------------------------------------
    r_fru = resposta.scores.get(FRUSTRACAO) if resposta else None
    frustracao = _limitar(r_fru.nivel, 0, 3) if r_fru else 0
    conf_fru = r_fru.confianca if r_fru else 0.0
    malicioso = float(resposta.nouls.get(MALICIOSO, 0.0)) if resposta else 0.0
    fora_de_escopo = float(resposta.nouls.get(FORA_DE_ESCOPO, 0.0)) if resposta else 0.0

    # --- 7. ações: UMA transferência no máximo (pedido > frustração > confiança)
    acao: Acao | None = None
    if (
        intencao == INTENCAO_PEDIR_ATENDENTE
        and origem_int == "jev"
        and conf_int >= cfg.limiar_pedir_atendente
    ):
        acao = Acao(
            "transferir", "pedido_cliente", f"cliente pediu atendente (confiança {conf_int:.2f})"
        )
    elif frustracao >= cfg.limiar_frustracao_score and conf_fru >= cfg.limiar_frustracao_confianca:
        acao = Acao(
            "transferir",
            "frustracao",
            f"frustração {frustracao}/3 com confiança {conf_fru:.2f} após resposta empática",
        )
    elif (
        decisao.categoria == CATEGORIA_INDEFINIDA
        and intencao == INTENCAO_RECLAMACAO
        and not decisao.precisa_esclarecer
        and cfg.acao_indefinida == "transferir"
    ):
        # TODO(P11): "transferir" × "triagem" para a categoria indefinida é decisão da Infominas.
        acao = Acao(
            "transferir", "confianca", "categoria indefinida após pergunta de esclarecimento"
        )

    # Intenção que fixou a categoria vigente: a do turno quando a decisão é nova;
    # a do episódio quando foi herdada/mantida (E3.10).
    herdou_categoria = decisao.origem_pergunta == "herdada" and anterior is not None
    resultado = Classificacao(
        intencao=intencao,
        intencao_categoria=(anterior.intencao if herdou_categoria and anterior else intencao),
        categoria=decisao.categoria,
        prioridade=prioridade,
        frustracao=frustracao,
        confiancas={
            "intencao": conf_int,
            "categoria": decisao.confianca,
            "prioridade": conf_prio,
            "frustracao": conf_fru,
        },
        origens={
            "intencao": origem_int_t,
            "categoria": decisao.origem_pergunta,
            "prioridade": origem_prio,
            "frustracao": "jev" if r_fru else "indefinida",
        },
        origem=decisao.origem,
        malicioso=malicioso,
        fora_de_escopo=fora_de_escopo,
        modelo=resposta.modelo if resposta else "",
        latencia_ms=int((time.perf_counter() - inicio) * 1000),
        jev_disponivel=jev_ok,
        precisa_esclarecer=decisao.precisa_esclarecer,
        candidatas=decisao.candidatas,
        confirmar_intencao=confirmar_intencao,
        tom_cuidadoso=acao is None and frustracao >= cfg.limiar_frustracao_cuidado,
        acoes=(acao,) if acao else (),
    )
    logger.info(
        "classificacao intencao=%s categoria=%s origem=%s prioridade=%s frustracao=%d "
        "jev=%s esclarecer=%s acoes=%s",
        resultado.intencao,
        resultado.categoria,
        resultado.origem,
        resultado.prioridade,
        resultado.frustracao,
        jev_ok,
        resultado.precisa_esclarecer,
        [a.origem for a in resultado.acoes],
    )
    return resultado


async def _decidir_categoria(
    *,
    resposta: RespostaJEV | None,
    fallback_llm: Callable[[], Any],
    taxonomia: Taxonomia,
    cfg: ConfigClassificacao,
    anterior: Classificacao | None,
    intencao: str,
    texto_atual: str,
    esclarecimentos_feitos: int,
) -> _DecisaoCategoria:
    r_cat = resposta.choices.get(CATEGORIA) if resposta else None
    if r_cat is not None and not taxonomia.existe(r_cat.escolha):
        logger.warning("jev_categoria_desconhecida valor=%r — tratada como ausente", r_cat.escolha)
        r_cat = None

    candidata: tuple[str, float, Origem] | None = None
    candidatas: tuple[str, ...] = ()

    if r_cat is not None:
        top2 = r_cat.top(2)
        candidatas = tuple(top2)
        if r_cat.confianca >= cfg.limiar_categoria_alto and r_cat.escolha != CATEGORIA_INDEFINIDA:
            candidata = (r_cat.escolha, r_cat.confianca, "jev")
        elif r_cat.confianca >= cfg.limiar_categoria_baixo:
            fb = await fallback_llm()
            if fb is not None and fb.categoria != CATEGORIA_INDEFINIDA and fb.categoria in top2:
                candidata = (fb.categoria, r_cat.confianca, "llm")
        # abaixo do limiar baixo: sem candidata → esclarecimento
    else:
        # JEV fora (ou sem a pergunta): o fallback LLM cobre (fail-soft).
        fb = await fallback_llm()
        if fb is not None and fb.categoria != CATEGORIA_INDEFINIDA:
            candidatas = (fb.categoria,)
            if cfg.fallback_fixa_sem_jev:
                candidata = (fb.categoria, 0.0, "llm")

    candidatas = tuple(c for c in candidatas if c != CATEGORIA_INDEFINIDA)
    anterior_fixa = (
        anterior is not None
        and anterior.categoria != CATEGORIA_INDEFINIDA
        and anterior.origem in ("jev", "llm")
    )
    # Pegajosa só contra troca de FILA de reclamação. Uma categoria fixada num turno
    # de informação (preço, boleto) não trava a seguinte: a reclamação que vier depois
    # define a fila do chamado, e dois pedidos de informação seguidos se corrigem sozinhos.
    # `anterior.intencao` é a intenção que FIXOU a categoria (sobrevive a turnos herdados).
    informativas = cfg.intencoes_com_categoria - {INTENCAO_RECLAMACAO}
    anterior_trava = (
        anterior is not None and anterior_fixa and anterior.intencao not in informativas
    )

    if candidata is not None:
        categoria, confianca, origem = candidata
        if anterior is not None and anterior_trava and categoria != anterior.categoria:
            # Pegajosa: só uma RECLAMAÇÃO troca a fila de uma reclamação aberta, e só com
            # correção explícita ou conf ≥ troca (do JEV). Pedido de informação nunca troca.
            troca = intencao == INTENCAO_RECLAMACAO and (
                corrigiu_explicitamente(texto_atual)
                or (origem == "jev" and confianca >= cfg.limiar_categoria_troca)
            )
            if not troca:
                return _DecisaoCategoria(
                    anterior.categoria, anterior.confianca, anterior.origem, "herdada", candidatas
                )
        return _DecisaoCategoria(categoria, confianca, origem, origem, candidatas)

    # Sem candidata nova: mantém a anterior — exceto quando ela veio de um pedido de
    # informação e o turno atual é uma RECLAMAÇÃO (a categoria de um boleto não serve
    # de fila para uma queda de internet: cai em esclarecimento/indefinida).
    if anterior is not None and anterior_fixa and (anterior_trava or intencao in informativas):
        return _DecisaoCategoria(
            anterior.categoria, anterior.confianca, anterior.origem, "herdada", candidatas
        )

    # Sem categoria e sem anterior fixa: esclarecer (uma vez) ou ficar indefinida.
    if intencao == INTENCAO_RECLAMACAO and esclarecimentos_feitos < cfg.max_esclarecimentos:
        return _DecisaoCategoria(
            CATEGORIA_INDEFINIDA,
            r_cat.confianca if r_cat else 0.0,
            "esclarecimento",
            "esclarecimento",
            candidatas,
            precisa_esclarecer=True,
        )
    return _DecisaoCategoria(
        CATEGORIA_INDEFINIDA,
        r_cat.confianca if r_cat else 0.0,
        "indefinida",
        "indefinida",
        candidatas,
    )


def _decidir_prioridade(
    *,
    resposta: RespostaJEV | None,
    taxonomia: Taxonomia,
    cfg: ConfigClassificacao,
    categoria: str,
    anterior: Classificacao | None,
    herdar: bool,
) -> tuple[str, float, OrigemPergunta]:
    if herdar and anterior is not None:
        return anterior.prioridade, 0.0, "herdada"

    r_prio = resposta.scores.get(PRIORIDADE) if resposta else None
    if r_prio is not None and r_prio.confianca >= cfg.limiar_prioridade:
        prioridade = PRIORIDADES[_limitar(r_prio.nivel, 0, len(PRIORIDADES) - 1)]
        origem: OrigemPergunta = "jev"
        confianca = r_prio.confianca
    else:
        prioridade = taxonomia.prioridade_padrao(categoria) or PRIORIDADE_PADRAO
        origem = "padrao"
        confianca = r_prio.confianca if r_prio else 0.0

    piso = taxonomia.prioridade_minima(categoria)
    if piso is not None and ordem_prioridade(prioridade) < ordem_prioridade(piso):
        prioridade, origem = piso, "regra"

    # Dentro da mesma categoria a prioridade só sobe (um "ok" calmo não rebaixa
    # um chamado já tratado como urgente). Hipótese de trabalho TODO(P8/P9),
    # configurável por JEV_PRIORIDADE_SO_SOBE.
    if (
        cfg.prioridade_so_sobe
        and anterior is not None
        and anterior.categoria == categoria
        and ordem_prioridade(anterior.prioridade) > ordem_prioridade(prioridade)
    ):
        prioridade, origem = anterior.prioridade, "herdada"
    return prioridade, confianca, origem


# ---------------------------------------------------------------------------
# Serviço (o que o grafo e o /classificar usam)
# ---------------------------------------------------------------------------


@dataclass
class ServicoClassificacao:
    """JEV + fallback + taxonomia + config, prontos para classificar um turno."""

    taxonomia: Taxonomia
    config: ConfigClassificacao
    jev: ClienteJEV | None = None
    fallback: FallbackLLM | None = None
    disponibilidade: JanelaDisponibilidade = field(default_factory=JanelaDisponibilidade)

    async def classificar(
        self,
        mensagens: Sequence[Mensagem],
        anterior: Classificacao | None = None,
        *,
        esclarecimentos_feitos: int = 0,
        nomes: Sequence[str] = (),
    ) -> Classificacao:
        return await classificar(
            mensagens,
            anterior,
            self.jev,
            self.fallback,
            taxonomia=self.taxonomia,
            config=self.config,
            esclarecimentos_feitos=esclarecimentos_feitos,
            nomes=nomes,
            disponibilidade=self.disponibilidade,
        )


def criar_servico_padrao(
    llm_factory: Callable[[], Any],
    config: ConfigClassificacao | None = None,
    taxonomia: Taxonomia | None = None,
) -> ServicoClassificacao:
    """Serviço de produção: JEV real (se houver chave) + fallback LLM estruturado.

    `llm_factory` devolve o chat model barato (`get_classifier_llm`); injetado
    para este pacote não conhecer o agente. Sem `TYPESAFE_API_KEY` (P8) não há
    cliente JEV: tudo cai no fallback LLM, com um aviso no boot.
    """
    cfg = config or get_config_classificacao()
    tax = taxonomia or carregar_taxonomia(cfg.taxonomia_path)
    jev: ClienteJEV | None = None
    if cfg.typesafe_api_key:
        jev = JEVTypeSafe(cfg)
    else:
        # TODO(P8): sem chave do JEV a classificação roda só no fallback LLM.
        logger.warning(
            "jev_sem_chave TYPESAFE_API_KEY ausente — classificação só pelo fallback LLM"
        )
    return ServicoClassificacao(
        taxonomia=tax,
        config=cfg,
        jev=jev,
        fallback=FallbackLLMEstruturado(llm_factory, tax),
        disponibilidade=JanelaDisponibilidade(cfg.jev_alerta_janela_s, cfg.jev_alerta_indisponivel),
    )
