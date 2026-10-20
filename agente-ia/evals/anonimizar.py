# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Anonimiza o export do histórico (P9) antes de virar dataset de eval.

    python -m evals.anonimizar evals/bruto/export.csv --saida evals/bruto/historico.jsonl \
        [--amostra 50] [--nomes config/nomes.txt] [--coluna-texto mensagem] [--semente 7]

Substituições (regra do projeto: PII vira token, nunca texto real no git):

| dado | token | detalhe |
|---|---|---|
| CNPJ (com/sem pontuação) | `<CNPJ>` | 14 dígitos, `00.000.000/0000-00` ou misto |
| CPF (com/sem pontuação) | `<CPF>` | 11 dígitos; bare 11 dígitos com DV **inválido** e cara de celular (DDD + 9) vira `<TEL>` |
| telefone BR | `<TEL>` | `+55`, DDD com/sem parênteses, 8/9 dígitos, com/sem máscara; 8 dígitos só com separador (`3333-4444`) |
| e-mail | `<EMAIL>` | |
| CEP | `<CEP>` | `00000-000` e 8 dígitos bare (sobre-mascarar é seguro: protocolo de 8 dígitos também vira `<CEP>`) |
| placa | `<PLACA>` | antiga `AAA-0000`/`AAA0000` e Mercosul `AAA0A00`, qualquer caixa |
| nome próprio | `<NOME>` | só os da lista `--nomes` (um por linha, `#` comenta), palavra inteira, sem distinção de caixa |

Entrada: `.csv` (uma linha por registro; toda célula de texto é anonimizada),
`.json` (lista de objetos, ou objeto com uma lista dentro) ou `.jsonl`. Saída:
JSONL com a mesma forma — ou, com `--coluna-texto`, o "pré-caso"
`{"id", "fonte": "historico", "estado": [texto], "esperado": null, "rotulador": null, "origem": {...}}`
que a rotulagem (planilha → script, E2.4/P9) completa. O relatório de
contagem por token sai no stdout; `--amostra N` imprime N registros (semente
fixa) para a **revisão manual obrigatória** antes de qualquer commit.

O arquivo bruto fica em `evals/bruto/` (gitignored e negado ao agente em
`.claude/settings.json`); o `config/nomes.txt` real também é gitignored.
"""

from __future__ import annotations

import argparse
import csv
import json
import random
import re
import sys
from collections import Counter
from collections.abc import Callable, Iterable, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

TOKENS: tuple[str, ...] = ("<EMAIL>", "<CNPJ>", "<CPF>", "<TEL>", "<CEP>", "<PLACA>", "<NOME>")

_RE_EMAIL = re.compile(r"[\w.+-]+@[\w-]+(?:\.[\w-]+)+", re.UNICODE)
_RE_CNPJ = re.compile(r"(?<!\d)\d{2}\.?\d{3}\.?\d{3}/?\d{4}-?\d{2}(?!\d)")
_RE_CPF = re.compile(r"(?<!\d)\d{3}\.?\d{3}\.?\d{3}-?\d{2}(?!\d)")
_RE_TEL = re.compile(
    r"(?<![\d.])"
    r"(?:"
    r"\+?55[\s.-]?\(?\d{2}\)?[\s.-]?9?[\s.-]?\d{4}[\s.-]?\d{4}"  # com código do país
    r"|\(?\d{2}\)?[\s.-]?9?[\s.-]?\d{4}[\s.-]?\d{4}"  # com DDD (10–11 dígitos)
    r"|\d{4,5}[\s.-]\d{4}"  # só o número, com separador
    r")"
    r"(?![\d.])"
)
_RE_CEP = re.compile(r"(?<!\d)(?:\d{5}-\d{3}|\d{8})(?!\d)")
_RE_PLACA = re.compile(
    r"(?<![A-Za-z0-9])(?:[A-Za-z]{3}-?\d{4}|[A-Za-z]{3}\d[A-Za-z]\d{2})(?![A-Za-z0-9])"
)
_RE_DDD_CELULAR = re.compile(r"^[1-9]\d9\d{8}$")


def cpf_valido(digitos: str) -> bool:
    """Dígitos verificadores do CPF (11 dígitos, sem pontuação)."""
    if len(digitos) != 11 or not digitos.isdigit() or digitos == digitos[0] * 11:
        return False
    for tamanho in (9, 10):
        soma = sum(
            int(d) * peso
            for d, peso in zip(digitos[:tamanho], range(tamanho + 1, 1, -1), strict=True)
        )
        dv = (soma * 10) % 11 % 10
        if dv != int(digitos[tamanho]):
            return False
    return True


def _trocar_cpf(m: re.Match[str]) -> str:
    bruto = m.group(0)
    digitos = re.sub(r"\D", "", bruto)
    if bruto != digitos:  # tinha pontuação: é CPF declarado
        return "<CPF>"
    if cpf_valido(digitos):
        return "<CPF>"
    if _RE_DDD_CELULAR.match(digitos):  # DDD + 9 + 8 dígitos, DV inválido: celular
        return "<TEL>"
    return "<CPF>"


@dataclass
class Anonimizador:
    """Aplica as substituições em ordem fixa e acumula contagens."""

    nomes: Sequence[str] = ()
    contagem: Counter[str] = field(default_factory=Counter)
    _re_nomes: re.Pattern[str] | None = field(default=None, init=False, repr=False)

    def __post_init__(self) -> None:
        limpos = sorted({n.strip() for n in self.nomes if n.strip()}, key=len, reverse=True)
        if limpos:
            alternativas = "|".join(re.escape(n) for n in limpos)
            self._re_nomes = re.compile(
                rf"(?<!\w)(?:{alternativas})(?!\w)", re.IGNORECASE | re.UNICODE
            )

    def _aplicar(
        self,
        texto: str,
        padrao: re.Pattern[str],
        token: str,
        trocar: Callable[[re.Match[str]], str] | None = None,
    ) -> str:
        def _sub(m: re.Match[str]) -> str:
            novo = trocar(m) if trocar else token
            self.contagem[novo] += 1
            return novo

        return padrao.sub(_sub, texto)

    def texto(self, texto: str) -> str:
        """Anonimiza uma string. Ordem importa: e-mail → CNPJ → CPF → telefone → CEP → placa → nomes."""
        t = self._aplicar(texto, _RE_EMAIL, "<EMAIL>")
        t = self._aplicar(t, _RE_CNPJ, "<CNPJ>")
        t = self._aplicar(t, _RE_CPF, "<CPF>", _trocar_cpf)
        t = self._aplicar(t, _RE_TEL, "<TEL>")
        t = self._aplicar(t, _RE_CEP, "<CEP>")
        t = self._aplicar(t, _RE_PLACA, "<PLACA>")
        if self._re_nomes is not None:
            t = self._aplicar(t, self._re_nomes, "<NOME>")
        return t

    def valor(self, valor: Any) -> Any:
        """Anonimiza recursivamente strings dentro de dict/list; outros tipos passam."""
        if isinstance(valor, str):
            return self.texto(valor)
        if isinstance(valor, dict):
            return {k: self.valor(v) for k, v in valor.items()}
        if isinstance(valor, list):
            return [self.valor(v) for v in valor]
        return valor

    def relatorio(self) -> str:
        linhas = ["substituições por tipo:"]
        for token in TOKENS:
            linhas.append(f"  {token:<8} {self.contagem.get(token, 0):>6}")
        linhas.append(f"  {'total':<8} {sum(self.contagem.values()):>6}")
        return "\n".join(linhas)


# --------------------------------------------------------------------- entrada
def carregar_nomes(caminho: Path | None) -> list[str]:
    if caminho is None or not caminho.exists():
        return []
    nomes: list[str] = []
    for linha in caminho.read_text(encoding="utf-8").splitlines():
        s = linha.strip()
        if s and not s.startswith("#"):
            nomes.append(s)
    return nomes


def ler_registros(entrada: Path) -> list[dict[str, Any]]:
    """CSV → lista de dicts por linha; JSON → lista de objetos; JSONL → um por linha."""
    sufixo = entrada.suffix.lower()
    if sufixo == ".csv":
        with entrada.open(encoding="utf-8-sig", newline="") as f:
            return [dict(linha) for linha in csv.DictReader(f)]
    if sufixo == ".jsonl":
        with entrada.open(encoding="utf-8") as f:
            return [json.loads(linha) for linha in f if linha.strip()]
    if sufixo == ".json":
        dado = json.loads(entrada.read_text(encoding="utf-8"))
        if isinstance(dado, list):
            return [d if isinstance(d, dict) else {"valor": d} for d in dado]
        if isinstance(dado, dict):
            listas = [v for v in dado.values() if isinstance(v, list)]
            if len(listas) == 1:
                return [d if isinstance(d, dict) else {"valor": d} for d in listas[0]]
            return [dado]
        raise ValueError(f"{entrada}: JSON deve ser lista ou objeto")
    raise ValueError(f"{entrada}: extensão não suportada ({sufixo}); use .csv, .json ou .jsonl")


def montar_pre_caso(indice: int, registro: dict[str, Any], coluna_texto: str) -> dict[str, Any]:
    """Converte um registro cru no esqueleto de `CasoClassificacao` a rotular."""
    if coluna_texto not in registro:
        raise KeyError(
            f"registro {indice}: coluna '{coluna_texto}' não existe ({sorted(registro)})"
        )
    texto = str(registro[coluna_texto] or "").strip()
    origem = {k: v for k, v in registro.items() if k != coluna_texto}
    return {
        "id": f"hist-{indice:04d}",
        "fonte": "historico",
        "estado": [linha for linha in texto.splitlines() if linha.strip()] or [texto],
        "esperado": None,
        "rotulador": None,
        "origem": origem,
    }


def anonimizar_registros(
    registros: Iterable[dict[str, Any]], anon: Anonimizador, coluna_texto: str | None = None
) -> list[dict[str, Any]]:
    saida: list[dict[str, Any]] = []
    for i, registro in enumerate(registros, start=1):
        limpo = anon.valor(registro)
        saida.append(montar_pre_caso(i, limpo, coluna_texto) if coluna_texto else limpo)
    return saida


def escrever_jsonl(caminho: Path, registros: Sequence[dict[str, Any]]) -> None:
    caminho.parent.mkdir(parents=True, exist_ok=True)
    with caminho.open("w", encoding="utf-8", newline="\n") as f:
        for r in registros:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")


# ------------------------------------------------------------------------- CLI
def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="python -m evals.anonimizar",
        description="Substitui PII por tokens num export (CSV/JSON/JSONL) e grava JSONL.",
    )
    p.add_argument(
        "entrada",
        type=Path,
        help="arquivo bruto (.csv, .json ou .jsonl), normalmente em evals/bruto/",
    )
    p.add_argument("--saida", type=Path, required=True, help="JSONL anonimizado a gravar")
    p.add_argument(
        "--amostra",
        type=int,
        default=0,
        metavar="N",
        help="imprime N registros para revisão manual",
    )
    p.add_argument(
        "--nomes",
        type=Path,
        default=None,
        help="lista de nomes próprios (default: config/nomes.txt se existir)",
    )
    p.add_argument(
        "--coluna-texto",
        default=None,
        help="coluna com a mensagem; gera o pré-caso de classificação",
    )
    p.add_argument("--semente", type=int, default=7, help="semente da amostra (reprodutível)")
    return p


def main(argv: Sequence[str] | None = None, saida_texto: Callable[[str], None] = print) -> int:
    args = _parser().parse_args(argv)
    entrada: Path = args.entrada
    if not entrada.exists():
        saida_texto(f"erro: entrada não existe: {entrada}")
        return 2
    caminho_nomes: Path | None = args.nomes
    if caminho_nomes is None:
        padrao = Path(__file__).resolve().parent.parent / "config" / "nomes.txt"
        caminho_nomes = padrao if padrao.exists() else None
    anon = Anonimizador(nomes=carregar_nomes(caminho_nomes))
    try:
        registros = ler_registros(entrada)
        limpos = anonimizar_registros(registros, anon, args.coluna_texto)
    except (ValueError, KeyError, json.JSONDecodeError) as e:
        saida_texto(f"erro: {e}")
        return 2
    escrever_jsonl(args.saida, limpos)
    saida_texto(f"{len(limpos)} registro(s) anonimizado(s) → {args.saida}")
    saida_texto(f"lista de nomes: {caminho_nomes if caminho_nomes else 'nenhuma (use --nomes)'}")
    saida_texto(anon.relatorio())
    if args.amostra > 0 and limpos:
        n = min(args.amostra, len(limpos))
        saida_texto(f"\n--- amostra de {n} para revisão manual (semente {args.semente}) ---")
        for r in random.Random(args.semente).sample(limpos, n):  # noqa: S311 - amostra, não segredo
            saida_texto(json.dumps(r, ensure_ascii=False))
    saida_texto(
        "\nREVISÃO MANUAL OBRIGATÓRIA antes de mover para evals/classificacao/historico.jsonl."
    )
    return 0


if __name__ == "__main__":
    _reconfigurar = getattr(sys.stdout, "reconfigure", None)
    if _reconfigurar is not None:
        _reconfigurar(encoding="utf-8")
    sys.exit(main())
