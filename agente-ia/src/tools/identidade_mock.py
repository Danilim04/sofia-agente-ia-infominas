"""Mock da identidade resolvida — placeholder do gate Go (Contrato A).

Em produção, o backend Go resolve a identidade (telefone → base própria →
login → Mongo da Azapfy → confirmação de um dado) e envia o **perfil mínimo**
no `POST /chat`. Aqui mockamos esse perfil para o harness de dev (Chainlit) e
para os testes, no MESMO formato do Contrato A. Quando o gate Go estiver
pronto, este módulo sai.

Formato (igual ao do Contrato A):
    {
      "encontrado": true,
      "login": "...", "nome": "...",
      "empresas": [
        {"grupo_empresa": "...", "grupo_user": "...", "area": "...",
         "bases": [{"nome": "...", "sigla": "...", "modulos_ativos": [...]}]}
      ]
    }
"""

from __future__ import annotations

import re
from typing import Any


IDENTIDADE_NAO_ENCONTRADA: dict[str, Any] = {"encontrado": False}


# Perfil mínimo derivado do doc de exemplo do Mongo: AZAPERS/MATRIZ estão
# ativos; o grupo AZAPFY é OMITIDO de propósito por estar inativo (ativo=false)
# — é o que a projeção real do gate vai produzir.
_IDENTIDADES_DEV: dict[str, dict[str, Any]] = {
    "11999990001": {
        "encontrado": True,
        "login": "10596693664",
        "nome": "Daniel Ferraz",
        "empresas": [
            {
                "grupo_empresa": "AZAPERS",
                "grupo_user": "COLABORADOR",
                "area": "SAC",
                "bases": [
                    {
                        "nome": "MATRIZ",
                        "sigla": "MAT",
                        "modulos_ativos": [
                            "cadastro_usuario",
                            "cadastro_relacao",
                            "pesquisa",
                            "dashboard",
                            "rota",
                            "romaneio",
                            "protocolo",
                            "pendencias",
                            "azp",
                            "cte",
                            "sac",
                            "mdfe",
                            "auditoria",
                            "ocorrencia",
                            "rastreamento",
                        ],
                    }
                ],
            }
        ],
    },
}


def _normalizar(chave: str) -> str:
    """Mantém só dígitos (telefone com máscara → só números)."""
    return re.sub(r"\D", "", chave or "")


def resolver_identidade_dev(chave: str) -> dict[str, Any]:
    """Resolve a identidade de dev por telefone (sem rede).

    Espelha o que o gate Go devolverá no Contrato A; chaves desconhecidas
    retornam `{"encontrado": False}`.
    """
    return _IDENTIDADES_DEV.get(_normalizar(chave), IDENTIDADE_NAO_ENCONTRADA)
