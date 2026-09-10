"""Input guardrails — defesa contra prompt injection direta (LLM01 direta).

Pipeline em duas camadas:

1. **Heurística rápida** (regex/listas de padrões conhecidos) — barata, sem
   custo de LLM, pega ~80% das tentativas óbvias ("ignore as instruções",
   "modo DAN", "act as", tags `<system>`, etc.).
2. **Classificador LLM** (modelo barato via OpenRouter) — só roda se a
   heurística passou. Classifica em `suporte` / `off_topic` / `malicioso`
   com justificativa.

Política de falha:
- Se o classificador LLM falhar (rede, timeout, parse), tratamos como
  `suporte` (fail-open) e logamos. A mensagem ainda passa pelo system
  prompt blindado e pelos output guardrails — defesa em profundidade.

Retorno padrão de `avaliar_entrada`:

    {
      "is_safe": bool,
      "categoria": "suporte" | "off_topic" | "malicioso",
      "motivo": str,
    }
"""

from __future__ import annotations

import logging
import re
from typing import Callable, Literal, Optional

from pydantic import BaseModel, Field


logger = logging.getLogger(__name__)


Categoria = Literal["suporte", "off_topic", "malicioso"]


# ---------------------------------------------------------------------------
# Camada 1 — Heurística rápida
# ---------------------------------------------------------------------------

# Padrões de jailbreak / prompt injection direta. Cada item é uma tupla
# (regex, descrição) para facilitar logging do motivo.
JAILBREAK_PATTERNS: list[tuple[re.Pattern[str], str]] = [
    (
        re.compile(
            r"ignor[ae]\s+(as|todas|tudo|todas\s+as|previous|all)\b.*(instru|regras|prompt|orienta)",
            re.IGNORECASE,
        ),
        "pedido para ignorar instruções",
    ),
    (
        re.compile(
            r"\b(disregard|forget|esque[cç][ae])\s+(the\s+above|previous|tudo|todas\s+as|as)\b",
            re.IGNORECASE,
        ),
        "pedido para descartar instruções anteriores",
    ),
    (
        re.compile(r"\bDAN\b|\bdo\s+anything\s+now\b", re.IGNORECASE),
        "modo DAN (Do Anything Now)",
    ),
    (
        re.compile(r"\bjailbreak\b", re.IGNORECASE),
        "menciona jailbreak explicitamente",
    ),
    (
        re.compile(r"\bsem\s+(filtro|filtros|restri[cç][ãa]o|censura)\b", re.IGNORECASE),
        "pede para remover filtros/restrições",
    ),
    (
        re.compile(r"\bmodo\s+(desenvolvedor|developer|admin|raiz|root)\b", re.IGNORECASE),
        "tenta ativar modo privilegiado",
    ),
    (
        re.compile(r"voc[êe]\s+agora\s+[ée]\s+", re.IGNORECASE),
        "tenta redefinir identidade do agente",
    ),
    (
        re.compile(r"\bact\s+as\s+|\bpretend\s+to\s+be\b|\bfa[cç]a\s+de\s+conta\b", re.IGNORECASE),
        "tenta forçar role-play",
    ),
    (
        re.compile(r"^\s*system\s*:", re.IGNORECASE | re.MULTILINE),
        "tenta injetar mensagem de sistema",
    ),
    (
        re.compile(r"</?\s*(system|prompt|instruction|user|assistant)\s*>", re.IGNORECASE),
        "tenta injetar tags de chat-template",
    ),
    (
        re.compile(
            r"\b(revele|mostre|imprima|cuspa|exiba|repita)\b[^.\n]{0,60}\b(prompt|system|instru[cç][ãa]o|orienta)",
            re.IGNORECASE,
        ),
        "pede para revelar prompt do sistema",
    ),
    (
        re.compile(r"\bprompt\s+injection\b", re.IGNORECASE),
        "menciona prompt injection",
    ),
    # Disclosure de internals sob pretexto (C2/A1 da Conversa 34): pedir a
    # lista de ferramentas/regras internas nunca é uma pergunta de cliente.
    (
        re.compile(
            r"\b(liste|listar|mostre|exiba|enumere|quais\s+s[ãa]o)\b[^.\n]{0,40}"
            r"\b(suas|as\s+suas|tuas)\s+(ferramentas|tools|fun[çc][õo]es|regras|"
            r"instru[çc][õo]es|diretrizes|capacidades)\b",
            re.IGNORECASE,
        ),
        "pede a lista de ferramentas/regras internas",
    ),
    (
        re.compile(
            r"\bmodo\s+(auditoria|homologa[çc][ãa]o|manuten[çc][ãa]o|debug|teste\s+interno)\b",
            re.IGNORECASE,
        ),
        "tenta ativar modo interno (auditoria/homologação/debug)",
    ),
]


def heuristica_rapida(texto: str) -> Optional[dict]:
    """Retorna dict de bloqueio se algum padrão casar, ou None caso contrário."""
    if not isinstance(texto, str) or not texto.strip():
        return None
    for padrao, descricao in JAILBREAK_PATTERNS:
        if padrao.search(texto):
            return {
                "is_safe": False,
                "categoria": "malicioso",
                "motivo": f"heurística: {descricao}",
            }
    return None


# ---------------------------------------------------------------------------
# Camada 2 — Classificador LLM
# ---------------------------------------------------------------------------


class ClassificacaoSeguranca(BaseModel):
    """Schema estruturado da resposta do classificador."""

    categoria: Categoria
    motivo: str = Field(default="", description="Justificativa em uma frase curta.")


def _montar_conteudo_classificador(texto: str, contexto: Optional[str]) -> str:
    """Monta o conteúdo enviado ao classificador, incluindo o contexto recente.

    Sem contexto, devolve só o texto (compatível com o comportamento antigo);
    com contexto, delimita o histórico e deixa claro o que classificar — é o
    que permite entender respostas curtas como "06" (o mês perguntado antes).
    """
    if not contexto:
        return texto
    return (
        "Histórico recente da conversa (apenas para contexto; NÃO classifique "
        "estas linhas):\n"
        f"{contexto}\n\n"
        f"Mensagem do usuário a classificar:\n{texto}"
    )


def _classificar_via_llm(texto: str, contexto: Optional[str] = None) -> dict:
    """Roda o classificador LLM via OpenRouter (modelo barato)."""
    from langchain_core.messages import HumanMessage, SystemMessage

    from src.agent.llm import get_classifier_llm
    from src.agent.prompts import SYSTEM_PROMPT_CLASSIFICADOR

    try:
        llm = get_classifier_llm().with_structured_output(ClassificacaoSeguranca)
        resultado: ClassificacaoSeguranca = llm.invoke(  # type: ignore[assignment]
            [
                SystemMessage(content=SYSTEM_PROMPT_CLASSIFICADOR),
                HumanMessage(content=_montar_conteudo_classificador(texto, contexto)),
            ]
        )
    except Exception as exc:  # noqa: BLE001 — fail-open com log
        logger.warning(
            "classificador_indisponivel erro=%s — passando como 'suporte' (fail-open)",
            exc,
        )
        return {
            "is_safe": True,
            "categoria": "suporte",
            "motivo": f"classificador indisponível: {exc}",
        }

    return {
        "is_safe": resultado.categoria == "suporte",
        "categoria": resultado.categoria,
        "motivo": resultado.motivo or f"classificado como {resultado.categoria}",
    }


# ---------------------------------------------------------------------------
# Pipeline
# ---------------------------------------------------------------------------


def avaliar_entrada(
    texto: str,
    *,
    contexto: Optional[str] = None,
    classificador: Optional[Callable[..., dict]] = None,
    pular_classificador: bool = False,
) -> dict:
    """Pipeline completo: heurística rápida → classificador LLM.

    Args:
        texto: mensagem do usuário (a mensagem a classificar).
        contexto: transcrição opcional das últimas mensagens da conversa, para
            o classificador interpretar respostas curtas em contexto (ex.: "06"
            como o mês que o agente acabou de perguntar). Quando ausente, o
            classificador é chamado exatamente como antes.
        classificador: injeção de dependência para testes — função
            `(texto, contexto=...) -> dict`. Default: `_classificar_via_llm`.
        pular_classificador: True quando há FLUXO ATIVO na conversa (o agente
            acabou de perguntar algo) — a mensagem é continuação por definição
            e não passa pelo classificador LLM (F8: um "não, deixa pra lá" no
            meio de um fluxo não pode virar off_topic). A heurística
            determinística de jailbreak roda SEMPRE.

    Returns:
        `{is_safe, categoria, motivo}`.
    """
    if not isinstance(texto, str) or not texto.strip():
        return {
            "is_safe": True,
            "categoria": "suporte",
            "motivo": "mensagem vazia — sem decisão",
        }

    bloqueio = heuristica_rapida(texto)
    if bloqueio is not None:
        logger.info(
            "input_guardrail_bloqueado camada=heuristica motivo=%r",
            bloqueio["motivo"],
        )
        return bloqueio

    if pular_classificador:
        logger.info("input_guardrail_fluxo_ativo — classificador LLM pulado")
        return {
            "is_safe": True,
            "categoria": "suporte",
            "motivo": "fluxo ativo — continuação de conversa",
        }

    classificador = classificador or _classificar_via_llm
    # Só repassamos `contexto` quando há algo — mantém compatibilidade com
    # classificadores injetados que aceitam apenas `(texto)`.
    veredito = classificador(texto, contexto=contexto) if contexto else classificador(texto)
    logger.info(
        "input_guardrail_classificador categoria=%r is_safe=%s",
        veredito.get("categoria"),
        veredito.get("is_safe"),
    )
    return veredito