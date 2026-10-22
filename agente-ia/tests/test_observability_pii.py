"""Máscara de PII de log e trace (E4.3): CPF, telefone e nome injetados."""

from __future__ import annotations

import io
import logging

import pytest

from src.observability.pii import (
    CAPACIDADE_NOMES,
    FormatadorPII,
    esquecer_nomes,
    instalar_mascara_pii,
    mascarar_dado,
    mascarar_telefone,
    mascarar_texto,
    nomes_conhecidos,
    registrar_nomes,
)

CPF = "529.982.247-25"
CPF_SO_DIGITOS = "52998224725"
TELEFONE = "+5531999990001"


@pytest.fixture(autouse=True)
def _sem_nomes():
    esquecer_nomes()
    yield
    esquecer_nomes()


def _logger_com_mascara(nome: str) -> tuple[logging.Logger, io.StringIO]:
    saida = io.StringIO()
    handler = logging.StreamHandler(saida)
    handler.setFormatter(logging.Formatter("%(asctime)s [%(levelname)s] %(name)s: %(message)s"))
    instalar_mascara_pii([handler])
    log = logging.getLogger(nome)
    log.handlers = [handler]
    log.propagate = False
    log.setLevel(logging.DEBUG)
    return log, saida


# ---------------------------------------------------------------------------
# texto
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "proibido", [CPF, CPF_SO_DIGITOS, TELEFONE, "31999990001", "(31) 99999-0001"]
)
def test_texto_livre_sem_cpf_nem_telefone(proibido):
    saida = mascarar_texto(f"meu documento/telefone é {proibido}, pode confirmar?")
    assert proibido not in saida
    assert "pode confirmar" in saida


def test_texto_livre_mascara_o_nome_conhecido_da_conversa():
    registrar_nomes("conv-1", ["Maria Souza"])
    saida = mascarar_texto("Oi Maria, aqui é a Sofia. Souza, tudo bem?")
    assert "Maria" not in saida and "Souza" not in saida
    assert "Sofia" in saida


def test_nome_explicito_funciona_sem_registro():
    assert "Joana" not in mascarar_texto("Joana ligou", ["Joana"])


def test_registro_de_nomes_e_limitado_e_descarta_os_mais_antigos():
    for i in range(CAPACIDADE_NOMES + 5):
        registrar_nomes(f"c{i}", [f"Nome{i}xyz"])
    conhecidos = nomes_conhecidos()
    assert len(conhecidos) == CAPACIDADE_NOMES
    assert "Nome0xyz" not in conhecidos
    assert f"Nome{CAPACIDADE_NOMES + 4}xyz" in conhecidos


def test_ids_de_correlacao_nao_sao_mascarados():
    saida = mascarar_texto("falha id_ocorrencia=SOF-20261021-1530-01 conversation_id=c1d2e3")
    assert "SOF-20261021-1530-01" in saida and "conversation_id=c1d2e3" in saida


def test_documento_externo_nao_vai_ao_log_nem_ao_trace():
    saida = mascarar_texto(
        "x <documento_externo>cliente Maria cpf 52998224725</documento_externo> y"
    )
    assert "Maria" not in saida and CPF_SO_DIGITOS not in saida
    assert "documento_externo omitido" in saida


def test_telefone_mascarado_deixa_so_tres_caracteres():
    assert mascarar_telefone(TELEFONE) == "*" * 11 + "001"
    assert mascarar_telefone(None) == ""


# ---------------------------------------------------------------------------
# dado estruturado (função `mask=` do Langfuse)
# ---------------------------------------------------------------------------


def test_dado_estruturado_mascara_por_chave_e_por_conteudo():
    registrar_nomes("c", ["Carla"])
    entrada = {
        "telefone": TELEFONE,
        "conversation_id": "c1d2",
        "x": f"meu cpf é {CPF}",
        "aninhado": {"cpf": CPF_SO_DIGITOS, "nome_contato": "Carla", "lista": [f"tel {TELEFONE}"]},
        "numerico": 52998224725,
        "tupla": ("Carla ligou",),
    }
    saida = mascarar_dado(data=entrada)
    texto = repr(saida)
    for proibido in (TELEFONE, CPF, CPF_SO_DIGITOS, "Carla"):
        assert proibido not in texto
    assert saida["conversation_id"] == "c1d2"
    assert saida["telefone"] == "<PII>"
    assert isinstance(saida["aninhado"]["lista"], list)


def test_dado_estruturado_com_numero_em_chave_de_pii_nao_escapa():
    assert mascarar_dado(data={"cpf": 52998224725}) == {"cpf": "<PII>"}


def test_dado_de_objeto_desconhecido_vira_texto_mascarado():
    class Coisa:
        def __repr__(self) -> str:
            return f"Coisa(cpf={CPF})"

    assert CPF not in repr(mascarar_dado(data=Coisa()))


def test_dado_cobre_mensagens_do_langchain():
    from langchain_core.messages import HumanMessage

    saida = mascarar_dado(data=[HumanMessage(content=f"cpf {CPF}")])
    assert CPF not in repr(saida)


def test_estrutura_profunda_demais_nao_estoura():
    ninho: dict = {}
    atual = ninho
    for _ in range(200):
        atual["n"] = {}
        atual = atual["n"]
    assert "profundidade" in repr(mascarar_dado(data=ninho))


# ---------------------------------------------------------------------------
# logging
# ---------------------------------------------------------------------------


def test_log_mascara_cpf_telefone_e_nome_em_qualquer_nivel():
    registrar_nomes("conv-9", ["Rafael"])
    log, saida = _logger_com_mascara("teste.pii.niveis")
    log.debug("debug cliente Rafael cpf %s", CPF)
    log.info("info telefone=%s", TELEFONE)
    log.warning("aviso %r", {"cpf": CPF_SO_DIGITOS})
    texto = saida.getvalue()
    for proibido in ("Rafael", CPF, CPF_SO_DIGITOS, TELEFONE):
        assert proibido not in texto
    assert texto.count("\n") == 3


def test_log_nao_mascara_o_prefixo_de_data():
    log, saida = _logger_com_mascara("teste.pii.prefixo")
    log.info("ok")
    assert saida.getvalue().startswith("20")
    assert "<NUM>" not in saida.getvalue()


def test_traceback_tambem_e_mascarado():
    log, saida = _logger_com_mascara("teste.pii.traceback")
    try:
        raise ValueError(f"cpf inválido {CPF}")
    except ValueError:
        log.exception("falhou")
    assert CPF not in saida.getvalue() and "ValueError" in saida.getvalue()


def test_mascara_nao_altera_o_registro_original(caplog):
    """O formatter trabalha numa cópia: o `caplog` (e handlers seguintes) veem o original."""
    log, _ = _logger_com_mascara("teste.pii.copia")
    log.addHandler(caplog.handler)
    log.info("cpf %s", CPF)
    assert CPF in caplog.records[-1].getMessage()


def test_mensagem_com_args_incompativeis_nao_derruba_o_log():
    log, saida = _logger_com_mascara("teste.pii.args")
    log.info("faltou %s %s", "um")  # noqa: PLE1206 - logging reporta o erro internamente; nunca levanta
    assert isinstance(saida.getvalue(), str)


def test_instalar_e_idempotente():
    handler = logging.StreamHandler(io.StringIO())
    instalar_mascara_pii([handler])
    primeiro = handler.formatter
    instalar_mascara_pii([handler])
    assert handler.formatter is primeiro and isinstance(primeiro, FormatadorPII)
