"""CPF e contrato (`src/identity`): dígitos verificadores, máscara e regex configurável."""

from __future__ import annotations

import pytest

from src.identity.config import ident_exigir_cpf
from src.identity.contrato import (
    CONTRATO_RE_PADRAO,
    contrato_valido,
    mascarar_contrato,
    normalizar_contrato,
)
from src.identity.cpf import cpf_valido, mascarar_cpf, somente_digitos

# CPFs fictícios com DV válido (o mesmo de `contratos/`).
VALIDOS = ["12345678909", "123.456.789-09", " 123.456.789-09 ", "52998224725", "529.982.247-25"]


@pytest.mark.parametrize("cpf", VALIDOS)
def test_cpf_valido(cpf):
    assert cpf_valido(cpf) is True


@pytest.mark.parametrize(
    "cpf",
    [
        "12345678900",  # DV errado
        "12345678908",  # 2º DV errado
        "11111111111",  # todos iguais passam no cálculo, mas não são CPF
        "00000000000",
        "1234567890",  # 10 dígitos
        "123456789090",  # 12 dígitos
        "",
        None,
        "abc",
        "CPF 12345678909",  # texto junto dos dígitos não "vira" CPF
        "12345678909 e 98765432100",
        "١٢٣٤٥٦٧٨٩٠٩",  # dígitos não ASCII
    ],
)
def test_cpf_invalido(cpf):
    assert cpf_valido(cpf) is False


def test_mascara_do_cpf_deixa_so_os_tres_digitos_do_meio_do_bloco_final():
    assert mascarar_cpf("12345678909") == "***.***.789-**"
    assert mascarar_cpf("123.456.789-09") == "***.***.789-**"


@pytest.mark.parametrize("lixo", ["", None, "123", "123456789012345"])
def test_mascara_de_entrada_que_nao_e_cpf_nao_vaza_nada(lixo):
    assert mascarar_cpf(lixo) == "***.***.***-**"


def test_mascara_nao_contem_o_cpf_completo():
    mascarado = mascarar_cpf("52998224725")
    assert "52998224725" not in mascarado
    assert mascarado.count("*") == 8


def test_somente_digitos():
    assert somente_digitos("123.456.789-09") == "12345678909"
    assert somente_digitos(None) == ""
    assert somente_digitos("a1b2") == "12"


# ---------------------------------------------------------------------------
# contrato — regex configurável (IDENT_CONTRATO_RE)
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("contrato", ["000123456", "1234", "123456789012", " 4821 "])
def test_contrato_valido_no_default(contrato, monkeypatch):
    monkeypatch.delenv("IDENT_CONTRATO_RE", raising=False)
    assert contrato_valido(contrato) is True


@pytest.mark.parametrize("contrato", ["", None, "123", "1234567890123", "12a45", "12 34"])
def test_contrato_invalido_no_default(contrato, monkeypatch):
    monkeypatch.delenv("IDENT_CONTRATO_RE", raising=False)
    assert contrato_valido(contrato) is False


def test_regex_do_contrato_e_configuravel(monkeypatch):
    monkeypatch.setenv("IDENT_CONTRATO_RE", r"^INF-\d{6}$")
    assert contrato_valido("INF-123456") is True
    assert contrato_valido("123456") is False


def test_regex_invalida_cai_no_default(monkeypatch):
    monkeypatch.setenv("IDENT_CONTRATO_RE", "([")
    assert contrato_valido("123456") is True
    assert CONTRATO_RE_PADRAO == r"^\d{4,12}$"


def test_mascara_do_contrato():
    assert mascarar_contrato("000123456") == "******456"
    assert mascarar_contrato("12") == "***"
    assert normalizar_contrato("  4821 ") == "4821"
    assert normalizar_contrato(None) == ""


# ---------------------------------------------------------------------------
# IDENT_EXIGIR_CPF (hipótese de plano, 07 N3)
# ---------------------------------------------------------------------------


def test_ident_exigir_cpf_default_false(monkeypatch):
    monkeypatch.delenv("IDENT_EXIGIR_CPF", raising=False)
    assert ident_exigir_cpf() is False


@pytest.mark.parametrize(("valor", "esperado"), [("true", True), ("1", True), ("false", False)])
def test_ident_exigir_cpf_le_a_variavel(monkeypatch, valor, esperado):
    monkeypatch.setenv("IDENT_EXIGIR_CPF", valor)
    assert ident_exigir_cpf() is esperado
