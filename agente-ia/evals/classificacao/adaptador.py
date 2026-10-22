# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Liga o runner ao `src.classificacao.classificar()` REAL (E2.4).

`ClassificadorReal` é o `Classificador` do runner (`list[str] → dict`) montado
sobre um `ServicoClassificacao`. Três modos, que só diferem em quem responde:

| modo | JEV | fallback LLM | precisa de | marca pytest |
|---|---|---|---|---|
| `jev` | `JEVTypeSafe` (real) | `FallbackLLMEstruturado` se houver `OPENROUTER_API_KEY` | `TYPESAFE_API_KEY` (P8) | `jev` |
| `llm` | nenhum | `FallbackLLMEstruturado` (real) | `OPENROUTER_API_KEY` | `llm_real` |
| `offline` | `JEVSimulado` | `FallbackSimulado` | nada | — |

O que o relatório mede (docs/plano/02, "Dataset"):

- `intencao` e `categoria` = a resposta BRUTA do JEV (escolha + `confidence`) — é
  isso que a cobertura@limiar, a acurácia@limiar e a calibração avaliam: o JEV
  decidiria sozinho quando `confidence ≥ limiar`. Sem JEV na rodada (`llm`, ou
  JEV fora do ar naquele turno), a resposta bruta é a do fallback, com confiança 0.
- `prioridade` = a prioridade FINAL (com o piso da taxonomia), confiança do Score.
- `frustracao`, `malicioso`, `fora_de_escopo` = respostas brutas do JEV.
- `categoria_final` = a categoria que o roteador fixou (só nos casos de
  reclamação, único caso em que o roteador classifica categoria): é o que o
  cliente vive, com fallback, esclarecimento e `indefinida`.
- `extras` = o que o roteador fez (fallback usado, esclarecimento, transferência…).

Um event loop próprio e único por classificador (o cliente `httpx` async do JEV
não atravessa loops); `fechar()` o encerra.
"""

from __future__ import annotations

import asyncio
import os
import time
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

from src.classificacao.config import ConfigClassificacao
from src.classificacao.fallback_llm import FallbackLLM, FallbackLLMEstruturado, ResultadoFallback
from src.classificacao.jev import ClienteJEV, JEVTypeSafe, mensagens_de_linhas
from src.classificacao.perguntas import (
    CATEGORIA,
    FORA_DE_ESCOPO,
    FRUSTRACAO,
    INTENCAO,
    MALICIOSO,
    PRIORIDADE,
    montar_perguntas,
)
from src.classificacao.roteador import ServicoClassificacao
from src.classificacao.taxonomia import Taxonomia, carregar_taxonomia
from src.classificacao.tipos import PRIORIDADES, Classificacao, Pergunta, RespostaJEV

MODOS_REAIS = ("jev", "llm")
MODOS = ("jev", "llm", "offline", "nulo")


class ErroModo(RuntimeError):
    """O modo pedido não pode rodar neste ambiente (chave ausente, SDK ausente…)."""


class GravadorJEV:
    """Passa a chamada ao JEV e guarda a última resposta e o estado enviado."""

    def __init__(self, interno: ClienteJEV) -> None:
        self._interno = interno
        self.ultima: RespostaJEV | None = None
        self.ultimo_estado = ""

    def reiniciar(self) -> None:
        self.ultima = None
        self.ultimo_estado = ""

    async def perguntar(self, estado: str, perguntas: Mapping[str, Pergunta]) -> RespostaJEV:
        self.ultimo_estado = estado
        self.ultima = None
        resposta = await self._interno.perguntar(estado, perguntas)
        self.ultima = resposta
        return resposta


class GravadorFallback:
    """Passa a chamada ao fallback LLM e guarda o último resultado."""

    def __init__(self, interno: FallbackLLM) -> None:
        self._interno = interno
        self.ultimo: ResultadoFallback | None = None
        self.ultimo_estado = ""

    def reiniciar(self) -> None:
        self.ultimo = None
        self.ultimo_estado = ""

    async def classificar(self, estado: str) -> ResultadoFallback:
        self.ultimo_estado = estado
        self.ultimo = None
        resultado = await self._interno.classificar(estado)
        self.ultimo = resultado
        return resultado


@dataclass(frozen=True)
class DescricaoModo:
    """O que a rodada realmente usou (vai para o contexto do relatório)."""

    modo: str
    classificador: str
    janela: int
    jev: str
    fallback: str
    simulado: bool
    avisos: tuple[str, ...] = ()


class ClassificadorReal:
    """`Classificador` do runner sobre `ServicoClassificacao` (ver docstring do módulo)."""

    def __init__(
        self,
        servico: ServicoClassificacao,
        gravador_jev: GravadorJEV | None,
        gravador_fb: GravadorFallback | None,
        *,
        latencia_simulada: bool = False,
    ) -> None:
        self._servico = servico
        self._jev = gravador_jev
        self._fb = gravador_fb
        self._latencia_simulada = latencia_simulada
        self._loop = asyncio.new_event_loop()

    def fechar(self) -> None:
        if not self._loop.is_closed():
            self._loop.close()

    def __call__(self, estado: list[str]) -> dict[str, Any]:
        if self._jev is not None:
            self._jev.reiniciar()
        if self._fb is not None:
            self._fb.reiniciar()
        mensagens = mensagens_de_linhas(estado)
        inicio = time.perf_counter()
        final = self._loop.run_until_complete(self._servico.classificar(mensagens))
        latencia_ms = (time.perf_counter() - inicio) * 1000.0
        bruto_jev = self._jev.ultima if self._jev else None
        bruto_fb = self._fb.ultimo if self._fb else None
        estado_enviado = (self._jev.ultimo_estado if self._jev else "") or (
            self._fb.ultimo_estado if self._fb else ""
        )
        if self._latencia_simulada:
            latencia_ms = float(bruto_jev.latencia_ms) if bruto_jev else 0.0
        return self._resposta(final, bruto_jev, bruto_fb, estado_enviado, latencia_ms)

    def _resposta(
        self,
        final: Classificacao,
        jev: RespostaJEV | None,
        fb: ResultadoFallback | None,
        estado_enviado: str,
        latencia_ms: float,
    ) -> dict[str, Any]:
        choice_int = jev.choices.get(INTENCAO) if jev else None
        choice_cat = jev.choices.get(CATEGORIA) if jev else None
        fb_ok = fb if (fb is not None and fb.ok) else None
        prio_jev = jev.scores.get(PRIORIDADE) if jev else None
        intencao = (
            choice_int.escolha if choice_int else (fb_ok.intencao if fb_ok else final.intencao)
        )
        categoria = (
            choice_cat.escolha if choice_cat else (fb_ok.categoria if fb_ok else final.categoria)
        )
        resposta: dict[str, Any] = {
            "intencao": intencao,
            "categoria": categoria,
            # nível BRUTO do JEV quando ele respondeu (é ele que a cobertura@limiar mede);
            # a prioridade final (default da categoria + piso da taxonomia) vai em `final`
            "prioridade": (
                PRIORIDADES[max(0, min(len(PRIORIDADES) - 1, prio_jev.nivel))]
                if prio_jev
                else final.prioridade
            ),
            "confiancas": {
                "intencao": choice_int.confianca if choice_int else 0.0,
                "categoria": choice_cat.confianca if choice_cat else 0.0,
                "prioridade": prio_jev.confianca if prio_jev else 0.0,
            },
            "final": {
                "intencao": final.intencao,
                "categoria": final.categoria,
                "prioridade": final.prioridade,
                "confianca": final.confianca,
                "confianca_prioridade": final.confiancas.get("prioridade", 0.0),
            },
            "latencia_ms": latencia_ms,
            "extras": {
                "fallback_chamado": fb is not None,
                "esclarecimento": final.precisa_esclarecer,
                "transferencia": bool(final.acoes),
                "estado_caracteres": float(len(estado_enviado)),
                **({"jev_indisponivel": jev is None} if self._servico.jev is not None else {}),
            },
        }
        if final.intencao == "reclamacao_problema":
            # Só em reclamação o roteador classifica categoria; nas demais "indefinida" é o esperado.
            resposta["extras"]["categoria_indefinida_em_reclamacao"] = (
                final.categoria == "indefinida"
            )
        if jev is not None:
            fru = jev.scores.get(FRUSTRACAO)
            if fru is not None:
                resposta["frustracao"] = fru.nivel
                resposta["confiancas"]["frustracao"] = fru.confianca
            if MALICIOSO in jev.nouls:
                resposta["malicioso"] = jev.nouls[MALICIOSO]
            if FORA_DE_ESCOPO in jev.nouls:
                resposta["fora_de_escopo"] = jev.nouls[FORA_DE_ESCOPO]
        return resposta


def _config(janela: int | None, *, isolada: bool) -> ConfigClassificacao:
    """`isolada=True` ignora `.env` E as variáveis `JEV_*`/`TAXONOMIA_PATH`/`TYPESAFE_API_KEY` do processo.

    O relatório offline não pode depender da máquina (limiares e janela do ambiente
    mudariam os números do exemplo commitado).
    """
    if not isolada:
        base = ConfigClassificacao()  # type: ignore[call-arg]
    else:
        guardadas = {
            k: v
            for k, v in os.environ.items()
            if k.upper().startswith("JEV_") or k.upper() in {"TAXONOMIA_PATH", "TYPESAFE_API_KEY"}
        }
        for k in guardadas:
            del os.environ[k]
        try:
            base = ConfigClassificacao(_env_file=None)  # type: ignore[call-arg]
        finally:
            os.environ.update(guardadas)
    return base.model_copy(update={"jev_janela_msgs": janela}) if janela else base


def _fallback_real(taxonomia: Taxonomia) -> FallbackLLM:
    from src.agent.llm import get_classifier_llm

    return FallbackLLMEstruturado(get_classifier_llm, taxonomia)


def criar_classificador(
    modo: str, janela: int | None = None
) -> tuple[ClassificadorReal, DescricaoModo]:
    """Monta o classificador do modo `jev` | `llm` | `offline`; `ErroModo` se faltar chave/SDK."""
    if modo not in ("jev", "llm", "offline"):
        raise ValueError(f"modo desconhecido: {modo}")
    cfg = _config(janela, isolada=modo == "offline")
    taxonomia = carregar_taxonomia(cfg.taxonomia_path)
    avisos: list[str] = []
    jev_cliente: ClienteJEV | None = None
    fb_cliente: FallbackLLM | None = None
    nome_jev, nome_fb = "nenhum", "nenhum"

    if modo == "offline":
        from evals.classificacao.simulado import FallbackSimulado, JEVSimulado

        jev_cliente = JEVSimulado(padrao=None)
        fb_cliente = FallbackSimulado(montar_perguntas(taxonomia))
        nome_jev, nome_fb = "JEVSimulado (FakeJEV lexical)", "FallbackSimulado (FakeFallbackLLM)"
    else:
        if modo == "jev":
            if not cfg.typesafe_api_key:
                raise ErroModo("--jev exige TYPESAFE_API_KEY (pendência P8: ainda não há chave)")
            jev_cliente = JEVTypeSafe(cfg)
            nome_jev = f"JEVTypeSafe ({cfg.jev_model})"
        if os.environ.get("OPENROUTER_API_KEY"):
            fb_cliente = _fallback_real(taxonomia)
            nome_fb = "FallbackLLMEstruturado (OpenRouter)"
        elif modo == "llm":
            raise ErroModo("--llm exige OPENROUTER_API_KEY")
        else:
            avisos.append(
                "sem OPENROUTER_API_KEY: rodada sem fallback LLM (baixa confiança do JEV cai em "
                "esclarecimento/indefinida; a cobertura@limiar e a calibração do JEV não mudam)"
            )

    gravador_jev = GravadorJEV(jev_cliente) if jev_cliente is not None else None
    gravador_fb = GravadorFallback(fb_cliente) if fb_cliente is not None else None
    servico = ServicoClassificacao(
        taxonomia=taxonomia, config=cfg, jev=gravador_jev, fallback=gravador_fb
    )
    descricao = DescricaoModo(
        modo=modo,
        classificador=modo,
        janela=cfg.jev_janela_msgs,
        jev=nome_jev,
        fallback=nome_fb,
        simulado=modo == "offline",
        avisos=tuple(avisos),
    )
    return ClassificadorReal(
        servico, gravador_jev, gravador_fb, latencia_simulada=modo == "offline"
    ), descricao
