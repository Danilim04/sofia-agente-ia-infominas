"""Máscara de PII (CPF, telefone, nome) para log e trace — regra 5 do CLAUDE.md.

Três consumidores, uma só regra:

- `FormatadorPII` (logging): mascara a MENSAGEM FORMATADA de todo registro, para o
  que um módulo esquecer de mascarar não chegar ao stdout/arquivo;
- `mascarar_dado` (Langfuse): é a função `mask=` do cliente — roda em todo
  `input`, `output` e `metadata` de todo span, inclusive o span de entrada de
  tool (`on_tool_start`), que leva o `telefone` injetado pela política;
- `mascarar_texto`: o texto livre (mensagem do cliente, resposta da Sofia).

Determinístico e sem rede: nada aqui depende do LLM. O texto livre usa o mesmo
mascarador do JEV (`classificacao.jev.mascarar_pii`: e-mail, telefone, CPF/CNPJ,
nº longo, nome após "meu nome é") mais os NOMES conhecidos das conversas ativas.
Nome só se mascara quando o sistema o conhece (`nome_contato` do Contrato A,
`nome_cadastro`): não existe regra confiável para achar nome em texto livre.
"""

from __future__ import annotations

import copy
import logging
import re
import threading
from collections import OrderedDict
from collections.abc import Iterable, Mapping
from typing import Any

from src.classificacao.jev import mascarar_pii

#: Chaves cujo VALOR é PII por definição (qualquer profundidade do dict).
CHAVES_PII = frozenset(
    {
        "telefone",
        "phone",
        "cpf",
        "contrato",
        "nome",
        "nome_contato",
        "nome_cadastro",
        "valor",  # `informar_identificacao(valor=...)`: CPF ou nº de contrato
        "email",
    }
)
VALOR_MASCARADO = "<PII>"
#: Bloco `<documento_externo>` (resposta de tool/RAG) pode trazer dado do cliente.
_DOCUMENTO_EXTERNO = re.compile(
    r"<documento_externo>.*?</documento_externo>", re.IGNORECASE | re.DOTALL
)
_DOCUMENTO_OMITIDO = "[documento_externo omitido]"
#: Teto de texto mascarado por chamada: o custo das regex não cresce com a entrada.
TETO_TEXTO = 20_000
#: Profundidade máxima da recursão em `mascarar_dado` (estruturas cíclicas/hostis).
PROFUNDIDADE_MAXIMA = 12

# ---------------------------------------------------------------------------
# Nomes conhecidos (conversas recentes)
# ---------------------------------------------------------------------------

#: Quantas conversas guardam seus nomes (LRU). Nomes de conversas antigas saem
#: da máscara — o span delas já foi enviado.
CAPACIDADE_NOMES = 1024

_nomes: OrderedDict[str, tuple[str, ...]] = OrderedDict()
_trava = threading.Lock()


def registrar_nomes(conversation_id: str, nomes: Iterable[str | None]) -> None:
    """Guarda os nomes do contato da conversa para a máscara de log/trace.

    O span é fechado em outra thread (exportador do Langfuse), então o registro
    é global e protegido por trava, não `contextvars`.
    """
    limpos = tuple(n.strip() for n in nomes if n and n.strip())
    if not limpos:
        return
    with _trava:
        _nomes[conversation_id] = limpos
        _nomes.move_to_end(conversation_id)
        while len(_nomes) > CAPACIDADE_NOMES:
            _nomes.popitem(last=False)


def nomes_conhecidos() -> tuple[str, ...]:
    with _trava:
        return tuple(n for grupo in _nomes.values() for n in grupo)


def esquecer_nomes() -> None:
    """Só para testes."""
    with _trava:
        _nomes.clear()


# ---------------------------------------------------------------------------
# Texto e dado estruturado
# ---------------------------------------------------------------------------

#: Trechos que NÃO são PII mas a máscara pegaria: IDs de correlação com cara de
#: nº longo (`SOF-20261021-1530-01`: senão o ID do log não casa com o do
#: gateway) e a apresentação da própria Sofia ("sou a Sofia").
_PROTEGIDOS = re.compile(
    r"\bSOF-\d{8}-\d{4}-\d{2,}\b|\b(?:conversation_id|id_ocorrencia)=[\w.:-]+"
    r"|(?i:\b(?:sou|aqui [ée]) (?:a|o) )Sofia\b"
)
_MARCA_INICIO, _MARCA_FIM = "", ""


def mascarar_texto(texto: object, nomes: Iterable[str] = ()) -> str:
    """Texto livre sem CPF, telefone, e-mail, nº longo nem nome conhecido."""
    if texto is None:
        return ""
    bruto = str(texto)[:TETO_TEXTO]
    bruto = _DOCUMENTO_EXTERNO.sub(_DOCUMENTO_OMITIDO, bruto)
    conhecidos = (*nomes, *nomes_conhecidos())
    guardados: list[str] = []

    def _guardar(m: re.Match[str]) -> str:
        guardados.append(m.group(0))
        return f"{_MARCA_INICIO}{len(guardados) - 1}{_MARCA_FIM}"

    mascarado = mascarar_pii(_PROTEGIDOS.sub(_guardar, bruto), conhecidos)
    for i, original in enumerate(guardados):
        mascarado = mascarado.replace(f"{_MARCA_INICIO}{i}{_MARCA_FIM}", original)
    return mascarado


def mascarar_telefone(telefone: str | None) -> str:
    """`*********789` — só os 3 últimos caracteres (mesma regra de `server._mascarar`)."""
    v = (telefone or "").strip()
    if len(v) <= 3:
        return "*" * len(v)
    return "*" * (len(v) - 3) + v[-3:]


def mascarar_dado(*, data: Any, **_: Any) -> Any:
    """Função `mask=` do Langfuse: devolve `data` sem PII, mantendo a estrutura.

    Fail-closed: objeto desconhecido vira texto mascarado; se algo estourar, o
    próprio Langfuse troca o campo por `<fully masked…>`.
    """
    return _mascarar(data, "", 0)


def _mascarar(valor: Any, chave: str, nivel: int) -> Any:
    if valor is None or isinstance(valor, bool):
        return valor
    if chave.lower() in CHAVES_PII:
        return VALOR_MASCARADO
    if nivel > PROFUNDIDADE_MAXIMA:
        return "<profundidade excedida>"
    if isinstance(valor, str):
        return mascarar_texto(valor)
    if isinstance(valor, int | float):
        # nº longo (CPF/telefone/contrato como int) é PII mesmo sob chave inocente
        return valor if len(str(valor)) < 9 else mascarar_texto(str(valor))
    if isinstance(valor, Mapping):
        return {str(k): _mascarar(v, str(k), nivel + 1) for k, v in valor.items()}
    if isinstance(valor, list | tuple | set | frozenset):
        return [_mascarar(v, "", nivel + 1) for v in valor]
    dump = getattr(valor, "model_dump", None)  # pydantic / mensagens do LangChain
    if callable(dump):
        try:
            return _mascarar(dump(), chave, nivel + 1)
        except Exception:
            return "<objeto não serializável>"
    return mascarar_texto(repr(valor))


# ---------------------------------------------------------------------------
# logging
# ---------------------------------------------------------------------------


class FormatadorPII(logging.Formatter):
    """Formatter que mascara a MENSAGEM (e o traceback) de todo registro.

    Envolve o formatter do handler e trabalha numa CÓPIA do registro: um
    `logging.Filter` mutaria o `LogRecord` compartilhado e os handlers seguintes
    (e o `caplog`) perderiam o original. O prefixo (data, nível, logger) fica de
    fora da máscara — `2026-10-21 15:30:01` tem cara de nº longo. O que um módulo
    esqueceu de mascarar (CPF numa exceção, telefone num `%r`) não sai.
    """

    def __init__(self, base: logging.Formatter | None = None) -> None:
        self._base = base or logging.Formatter()
        super().__init__()

    def format(self, record: logging.LogRecord) -> str:
        copia = copy.copy(record)
        try:
            mensagem = record.getMessage()
        except Exception:  # args incompatíveis com a mensagem: nunca derrubar o log
            mensagem = str(record.msg)
        copia.msg, copia.args = mascarar_texto(mensagem), None
        if record.exc_info:
            copia.exc_text = mascarar_texto(self._base.formatException(record.exc_info))
            copia.exc_info = None
        elif record.exc_text:
            copia.exc_text = mascarar_texto(record.exc_text)
        return self._base.format(copia)


def instalar_mascara_pii(handlers: Iterable[logging.Handler] | None = None) -> None:
    """Põe `FormatadorPII` nos handlers (default: os do logger-raiz). Idempotente."""
    for handler in handlers if handlers is not None else logging.getLogger().handlers:
        if not isinstance(handler.formatter, FormatadorPII):
            handler.setFormatter(FormatadorPII(handler.formatter))
