"""Configuração da identificação no cérebro (espelha o gateway Go).

Lê as MESMAS variáveis do gateway (`IDENT_EXIGIR_CPF`, `IDENT_CONTRATO_RE`) do
ambiente E do `agente-ia/.env` (como o resto do cérebro, via pydantic-settings;
o ambiente vence o arquivo). Sem isso, quem configurar só no `.env` teria o Go
aplicando a regra e o Python ignorando. Instanciado a cada chamada (barato) para
que a mudança de variável valha sem reiniciar e os testes possam trocá-la.
"""

from __future__ import annotations

import logging

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict

logger = logging.getLogger(__name__)

#: Default do plano (`docs/plano/01-ferramentas.md`, A1): 4 a 12 dígitos.
CONTRATO_RE_PADRAO = r"^\d{4,12}$"


class ConfigIdentificacao(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env", env_file_encoding="utf-8", extra="ignore", case_sensitive=False
    )

    #: `IDENT_EXIGIR_CPF` (default **false**): telefone reconhecido pelo lookup do
    #: Infodesk dispensa CPF/contrato (`docs/infodesk/07` N3, hipótese de plano;
    #: ADR 0004, revisão 2026-10-20). Com `true` o lookup não basta e a tool
    #: `informar_identificacao` volta a ser obrigatória.
    #: TODO(N3): a Infominas decide se telefone conhecido basta para abrir chamado.
    ident_exigir_cpf: bool = Field(False, alias="IDENT_EXIGIR_CPF")
    #: `IDENT_CONTRATO_RE`: formato do número de contrato (hipótese; TODO(N3)).
    ident_contrato_re: str = Field(CONTRATO_RE_PADRAO, alias="IDENT_CONTRATO_RE")


def carregar() -> ConfigIdentificacao:
    """Config vigente; valor inválido cai nos defaults (nunca derruba o turno)."""
    try:
        return ConfigIdentificacao()  # type: ignore[call-arg]
    except Exception as exc:
        logger.warning("config de identificação inválida; usando defaults (%s)", type(exc).__name__)
        return ConfigIdentificacao.model_construct(
            ident_exigir_cpf=False, ident_contrato_re=CONTRATO_RE_PADRAO
        )


def ident_exigir_cpf() -> bool:
    return carregar().ident_exigir_cpf
