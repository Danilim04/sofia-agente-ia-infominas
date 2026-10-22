"""Configuração da classificação (JEV + roteador) — tudo vem do ambiente.

Limiares e parâmetros do `docs/plano/02-classificacao.md` ficam aqui, com os
defaults do plano, para serem calibrados com dados próprios (P8/P9) sem mexer
em código. Módulo próprio (e não `src/config.py`) para a classificação ser
autocontida: o pacote `src.classificacao` não conhece o agente.

Todas as variáveis têm prefixo `JEV_` (limiares: `JEV_LIMIAR_*`), exceto a
chave do serviço (`TYPESAFE_API_KEY`, nome que o próprio SDK já lê).
"""

from __future__ import annotations

from functools import lru_cache
from pathlib import Path
from typing import Literal

from pydantic import Field, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict

from src.classificacao.tipos import INTENCOES, INTENCOES_COM_CATEGORIA

#: Raiz do pacote do cérebro (`agente-ia/`): `src/classificacao/config.py` → 3 níveis.
RAIZ_AGENTE = Path(__file__).resolve().parents[2]


class ConfigClassificacao(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
        case_sensitive=False,
        populate_by_name=True,
    )

    # --- Serviço JEV (TypeSafe System One) ---
    #: Sem chave não há chamada ao JEV: o cérebro cai direto no fallback LLM
    #: (fail-soft). TODO(P8): chave, limites e desempenho em pt-BR.
    typesafe_api_key: str = Field("", alias="TYPESAFE_API_KEY")
    #: Modelo PINADO — os limiares abaixo são calibrados para ele (docs/plano/02).
    jev_model: str = Field("jev-1.13.0", alias="JEV_MODEL")
    jev_timeout: float = Field(2.0, alias="JEV_TIMEOUT")
    jev_max_retries: int = Field(2, alias="JEV_MAX_RETRIES")
    #: Teto da chamada INTEIRA (tentativas + esperas): o turno não espera mais que isto.
    jev_prazo_total: float = Field(6.0, alias="JEV_PRAZO_TOTAL")
    #: Disjuntor: após N falhas seguidas, pula o JEV por `pausa` segundos (vai
    #: direto ao fallback LLM) e testa de novo depois.
    jev_disjuntor_falhas: int = Field(5, alias="JEV_DISJUNTOR_FALHAS")
    jev_disjuntor_pausa: float = Field(30.0, alias="JEV_DISJUNTOR_PAUSA")
    #: Alerta quando a fração de turnos sem JEV na janela passa deste valor (02: 5 %).
    jev_alerta_indisponivel: float = Field(0.05, alias="JEV_ALERTA_INDISPONIVEL")
    jev_alerta_janela_s: float = Field(900.0, alias="JEV_ALERTA_JANELA_S")

    # --- Estado enviado ao JEV ---
    #: C-JEV1: nº de mensagens da transcrição (INCLUI a mensagem atual). Hipótese
    #: de trabalho; validar com os evals (janelas 1, 3, 6, 10) em E2.4.
    jev_janela_msgs: int = Field(6, alias="JEV_JANELA_MSGS")
    jev_max_tokens_estado: int = Field(2000, alias="JEV_MAX_TOKENS_ESTADO")

    # --- Limiares do roteador (tabela de docs/plano/02) ---
    limiar_intencao_alto: float = Field(0.70, alias="JEV_LIMIAR_INTENCAO_ALTO")
    limiar_intencao_baixo: float = Field(0.45, alias="JEV_LIMIAR_INTENCAO_BAIXO")
    limiar_categoria_alto: float = Field(0.65, alias="JEV_LIMIAR_CATEGORIA_ALTO")
    limiar_categoria_baixo: float = Field(0.40, alias="JEV_LIMIAR_CATEGORIA_BAIXO")
    #: Confiança mínima para a categoria NOVA substituir uma já fixada (pegajosa).
    limiar_categoria_troca: float = Field(0.85, alias="JEV_LIMIAR_CATEGORIA_TROCA")
    limiar_prioridade: float = Field(0.60, alias="JEV_LIMIAR_PRIORIDADE")
    #: Nível (0–3) a partir do qual a frustração dispara a transferência...
    limiar_frustracao_score: int = Field(3, alias="JEV_LIMIAR_FRUSTRACAO_SCORE")
    #: ...desde que a confiança chegue a este valor.
    limiar_frustracao_confianca: float = Field(0.60, alias="JEV_LIMIAR_FRUSTRACAO_CONFIANCA")
    #: Nível a partir do qual o prompt ganha tom mais cuidadoso.
    limiar_frustracao_cuidado: int = Field(2, alias="JEV_LIMIAR_FRUSTRACAO_CUIDADO")
    limiar_malicioso_bloqueio: float = Field(0.80, alias="JEV_LIMIAR_MALICIOSO_BLOQUEIO")
    limiar_malicioso_aviso: float = Field(0.50, alias="JEV_LIMIAR_MALICIOSO_AVISO")
    limiar_fora_escopo: float = Field(0.80, alias="JEV_LIMIAR_FORA_ESCOPO")
    limiar_pedir_atendente: float = Field(0.60, alias="JEV_LIMIAR_PEDIR_ATENDENTE")

    # --- Hipóteses de trabalho configuráveis ---
    #: Máx. de perguntas de esclarecimento por episódio (02: "máx. 1").
    max_esclarecimentos: int = Field(1, alias="JEV_MAX_ESCLARECIMENTOS")
    #: O que fazer com a categoria `indefinida` DEPOIS do esclarecimento
    #: (02: "mesa de triagem (P11) ou transferência"). TODO(P11): decidir com a
    #: Infominas. "transferir" → `acoes: [transferir(confianca)]`; "triagem" →
    #: segue como `indefinida` (o gateway usa a mesa de triagem da taxonomia).
    acao_indefinida: Literal["transferir", "triagem"] = Field(
        "transferir", alias="JEV_ACAO_INDEFINIDA"
    )
    #: Com o JEV fora do ar, o fallback LLM sozinho (sem corroboração do JEV)
    #: pode fixar a categoria? Hipótese: sim, com `origem: llm`.
    fallback_fixa_sem_jev: bool = Field(True, alias="JEV_FALLBACK_FIXA_SEM_JEV")

    #: Dentro da mesma categoria a prioridade só sobe (um "ok" calmo não rebaixa
    #: um chamado já urgente). Hipótese de trabalho. TODO(P8/P9): validar com o
    #: histórico real; `false` recalcula a cada reclamação.
    prioridade_so_sobe: bool = Field(True, alias="JEV_PRIORIDADE_SO_SOBE")

    #: Intenções em que a categoria é decidida (E3.10), separadas por vírgula. A
    #: categoria fixada fora de reclamação serve ao roteamento do handoff e ao
    #: contexto do prompt; abrir chamado segue exigindo categoria (política das tools).
    intencoes_com_categoria_csv: str = Field(
        ",".join(sorted(INTENCOES_COM_CATEGORIA, key=INTENCOES.index)),
        alias="JEV_INTENCOES_COM_CATEGORIA",
    )

    # --- Taxonomia ---
    taxonomia_path: Path = Field(RAIZ_AGENTE / "config" / "taxonomia.yaml", alias="TAXONOMIA_PATH")

    @field_validator("intencoes_com_categoria_csv")
    @classmethod
    def _intencoes_validas(cls, valor: str) -> str:
        nomes = [n.strip() for n in valor.split(",") if n.strip()]
        invalidas = [n for n in nomes if n not in INTENCOES]
        if invalidas or not nomes:
            raise ValueError(
                f"JEV_INTENCOES_COM_CATEGORIA: intenções inválidas {invalidas} "
                f"(aceitas: {', '.join(INTENCOES)}; ao menos uma)"
            )
        return ",".join(nomes)

    @property
    def intencoes_com_categoria(self) -> frozenset[str]:
        return frozenset(self.intencoes_com_categoria_csv.split(","))


@lru_cache
def get_config_classificacao() -> ConfigClassificacao:
    return ConfigClassificacao()  # type: ignore[call-arg]
