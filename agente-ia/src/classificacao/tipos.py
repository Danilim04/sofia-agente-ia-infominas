"""Tipos da classificação — dataclasses puras, sem dependência de SDK nem do agente.

- `Mensagem`: uma linha da transcrição (cliente ou Sofia).
- `Choice`/`Score`/`Noul`: as perguntas tipadas enviadas ao JEV (espelham os
  tipos do SDK da TypeSafe, que só é importado em `jev.py`, de forma tardia —
  assim o roteador e os testes rodam sem o SDK instalado).
- `RespostaJEV`: o que voltou do JEV (ou de um `FakeJEV`), já normalizado.
- `Classificacao`: o resultado do turno (o que o roteador decidiu).
- `Acao`: efeito pedido ao gateway (hoje só `transferir`).

Ver `docs/plano/02-classificacao.md`.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any, Final, Literal

# ---------------------------------------------------------------------------
# Vocabulário fechado
# ---------------------------------------------------------------------------

INTENCAO_RECLAMACAO: Final = "reclamacao_problema"
INTENCAO_DUVIDA: Final = "duvida_informacao"
INTENCAO_STATUS: Final = "status_chamado"
INTENCAO_PEDIR_ATENDENTE: Final = "pedir_atendente"
INTENCAO_SOCIAL: Final = "saudacao_social"
INTENCAO_CONFIRMACAO: Final = "confirmacao"
INTENCAO_OUTRO: Final = "outro"

#: Valores da pergunta `intencao` (Choice). Fonte única — `perguntas.py` e o
#: schema do fallback LLM derivam daqui.
INTENCOES: Final[tuple[str, ...]] = (
    INTENCAO_RECLAMACAO,
    INTENCAO_DUVIDA,
    INTENCAO_STATUS,
    INTENCAO_PEDIR_ATENDENTE,
    INTENCAO_SOCIAL,
    INTENCAO_CONFIRMACAO,
    INTENCAO_OUTRO,
)

#: DEFAULT das intenções em que a categoria é (re)calculada (E3.10): reclamação
#: e os pedidos de informação/serviço (`outro`, `duvida_informacao`), para que um
#: boleto ou um preço também cheguem à transferência com categoria e sigam ao
#: departamento da categoria. Configurável em `JEV_INTENCOES_COM_CATEGORIA`
#: (`ConfigClassificacao.intencoes_com_categoria`). Nas demais (status, saudação,
#: "sim/ok", pedido de atendente) a categoria já fixada é herdada: um "ok" não
#: apaga a classificação. A PERGUNTA de esclarecimento por baixa confiança só
#: existe em reclamação (quem só pediu informação não é indagado sobre categoria).
INTENCOES_COM_CATEGORIA: Final[frozenset[str]] = frozenset(
    {INTENCAO_RECLAMACAO, INTENCAO_OUTRO, INTENCAO_DUVIDA}
)

#: Ordem canônica das prioridades da Sofia (índice = nível do Score do JEV).
PRIORIDADES: Final[tuple[str, ...]] = ("baixa", "media", "alta", "urgente")
PRIORIDADE_PADRAO: Final = "media"

CATEGORIA_INDEFINIDA: Final = "indefinida"

Origem = Literal["jev", "llm", "esclarecimento", "indefinida"]

#: Origem do valor de UMA pergunta (auditoria): além de jev/llm, "regra"
#: (piso de prioridade da categoria) e "padrao" (default por baixa confiança).
OrigemPergunta = Literal["jev", "llm", "regra", "padrao", "esclarecimento", "indefinida", "herdada"]

AutorMensagem = Literal["cliente", "sofia"]

MotivoTransferencia = Literal["frustracao", "confianca", "pedido_cliente"]


def ordem_prioridade(nome: str) -> int:
    """Posição de `nome` em `PRIORIDADES` (`ValueError` se desconhecida)."""
    return PRIORIDADES.index(nome)


# ---------------------------------------------------------------------------
# Entrada
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Mensagem:
    """Uma mensagem da transcrição. Só texto de cliente/Sofia — nunca tool/RAG."""

    autor: AutorMensagem
    texto: str


# ---------------------------------------------------------------------------
# Perguntas ao JEV
# ---------------------------------------------------------------------------

#: `criteria` de um Choice: nome → descrição (str), objeto estruturado
#: (`{"what", "examples", "not_for"}`) ou `None`.
CriterioChoice = str | Mapping[str, Any] | None


@dataclass(frozen=True)
class Choice:
    instrucoes: str
    criterios: Mapping[str, CriterioChoice]


@dataclass(frozen=True)
class Score:
    """Nível ordinal: o índice do critério é o nível (0 = primeiro)."""

    instrucoes: str
    criterios: Sequence[str]


@dataclass(frozen=True)
class Noul:
    """Probabilidade (0–1) de a afirmação ser verdadeira."""

    instrucoes: str


Pergunta = Choice | Score | Noul


# ---------------------------------------------------------------------------
# Saída do JEV
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class RespostaChoice:
    escolha: str
    confianca: float
    probabilidades: Mapping[str, float] = field(default_factory=dict)

    def top(self, n: int = 2) -> list[str]:
        """As `n` opções mais prováveis (a escolhida primeiro, mesmo sem probabilidades)."""
        ranking = sorted(self.probabilidades.items(), key=lambda kv: kv[1], reverse=True)
        nomes = [self.escolha] + [nome for nome, _ in ranking if nome != self.escolha]
        return nomes[:n]


@dataclass(frozen=True)
class RespostaScore:
    nivel: int
    confianca: float
    #: Score esperado (média ponderada pelas probabilidades; pode ser fracionário).
    score: float = 0.0


@dataclass(frozen=True)
class RespostaJEV:
    choices: Mapping[str, RespostaChoice] = field(default_factory=dict)
    scores: Mapping[str, RespostaScore] = field(default_factory=dict)
    #: Probabilidade 0–1 por pergunta Noul.
    nouls: Mapping[str, float] = field(default_factory=dict)
    modelo: str = ""
    latencia_ms: int = 0


# ---------------------------------------------------------------------------
# Resultado do turno
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Acao:
    """Ação pedida ao gateway (Contrato A, `ChatResponse.acoes`)."""

    tipo: Literal["transferir"]
    origem: MotivoTransferencia
    motivo: str

    def como_dict(self) -> dict[str, str]:
        return {"tipo": self.tipo, "origem": self.origem, "motivo": self.motivo}


@dataclass(frozen=True)
class Classificacao:
    """O que o roteador decidiu para o turno.

    `categoria`/`prioridade` são o que as tools e o gateway consomem (via
    `para_estado`); o restante é auditoria e orientação do prompt. Imutável: o
    estado persistido guarda `para_estado()`, nunca o objeto.
    """

    intencao: str
    categoria: str
    prioridade: str
    #: 0–3 (Score do JEV); 0 quando o JEV não respondeu.
    frustracao: int = 0
    #: Confiança por pergunta: intencao, categoria, prioridade, frustracao.
    confiancas: Mapping[str, float] = field(default_factory=dict)
    #: Origem por pergunta (mesmas chaves de `confiancas`).
    origens: Mapping[str, OrigemPergunta] = field(default_factory=dict)
    #: Origem da CATEGORIA (a que vai ao contrato).
    origem: Origem = "indefinida"
    malicioso: float = 0.0
    fora_de_escopo: float = 0.0
    modelo: str = ""
    latencia_ms: int = 0
    jev_disponivel: bool = False
    #: Pedir UMA pergunta de esclarecimento neste turno (categoria ambígua).
    precisa_esclarecer: bool = False
    #: Candidatas da categoria (1ª e 2ª do JEV) para guiar o esclarecimento.
    candidatas: tuple[str, ...] = ()
    #: Intenção com confiança entre `baixo` e `alto`: o prompt manda confirmar.
    confirmar_intencao: bool = False
    #: Frustração ≥ nível de cuidado (e abaixo da transferência): tom mais cuidadoso.
    tom_cuidadoso: bool = False
    acoes: tuple[Acao, ...] = ()

    @property
    def confianca(self) -> float:
        """Confiança da categoria (a que vai ao contrato como `confianca`)."""
        return float(self.confiancas.get("categoria", 0.0))

    def para_estado(self) -> dict[str, Any]:
        """Forma persistida em `state.classificacao` e devolvida no Contrato A.

        EXATAMENTE as 5 chaves do contrato (`contratos/contrato-a/chat-response.json`);
        a política das tools injeta este dict (`categoria`, `prioridade`,
        `confianca`, `origem`).
        """
        return {
            "intencao": self.intencao,
            "categoria": self.categoria,
            "prioridade": self.prioridade,
            "confianca": round(self.confianca, 4),
            "origem": self.origem,
        }

    @classmethod
    def de_estado(cls, dado: Mapping[str, Any] | None) -> Classificacao | None:
        """Reconstrói o mínimo para a pegajosidade a partir de `state.classificacao`."""
        if not dado or not dado.get("categoria"):
            return None
        origem = str(dado.get("origem") or "indefinida")
        if origem not in ("jev", "llm", "esclarecimento", "indefinida"):
            origem = "indefinida"
        prioridade = str(dado.get("prioridade") or PRIORIDADE_PADRAO)
        if prioridade not in PRIORIDADES:
            prioridade = PRIORIDADE_PADRAO
        try:
            confianca = float(dado.get("confianca") or 0.0)
        except (TypeError, ValueError):
            confianca = 0.0
        return cls(
            intencao=str(dado.get("intencao") or "outro"),
            categoria=str(dado["categoria"]),
            prioridade=prioridade,
            confiancas={"categoria": confianca},
            origem=origem,  # type: ignore[arg-type]  # validado acima
        )
