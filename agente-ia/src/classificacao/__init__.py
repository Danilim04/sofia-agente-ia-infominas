"""Classificação determinística (JEV) com fallback LLM — ver docs/plano/02-classificacao.md.

Módulos:
- `tipos`: dataclasses (`Classificacao`, `Mensagem`, perguntas e respostas do JEV).
- `taxonomia`: carrega/valida `config/taxonomia.yaml` e gera os `criteria` do JEV.
- `perguntas`: as 6 perguntas enviadas ao JEV (uma chamada por turno).
- `jev`: cliente (`JEVTypeSafe`), `estado_para_jev` (PII mascarada), disjuntor.
- `fallback_llm`: classificador LLM estruturado (mesmas classes da taxonomia).
- `roteador`: limiares, pegajosidade e `acoes` — `classificar()`.
- `config`: `JEV_*` / `JEV_LIMIAR_*` / `TAXONOMIA_PATH`.

Este pacote NÃO conhece o agente, as tools nem o RAG (contrato de arquitetura
em `pyproject.toml`): o grafo (`src/agent/nodes.py`) é quem o chama.
"""

from src.classificacao.roteador import (
    ServicoClassificacao,
    classificar,
    corrigiu_explicitamente,
    criar_servico_padrao,
)
from src.classificacao.taxonomia import ErroTaxonomia, Taxonomia, carregar_taxonomia
from src.classificacao.tipos import Acao, Classificacao, Mensagem

__all__ = [
    "Acao",
    "Classificacao",
    "ErroTaxonomia",
    "Mensagem",
    "ServicoClassificacao",
    "Taxonomia",
    "carregar_taxonomia",
    "classificar",
    "corrigiu_explicitamente",
    "criar_servico_padrao",
]
