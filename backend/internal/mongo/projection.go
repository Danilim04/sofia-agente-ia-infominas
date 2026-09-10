package mongo

import "sort"

// Projetar achata o documento do Mongo no perfil mínimo:
//   - só grupos com ativo==true viram empresas;
//   - só módulos com ativo==true entram em modulos_ativos.
//
// A saída é determinística (ordenada por grupo, base e módulo) para facilitar
// testes e cache. Descarta tudo o mais do documento (cpf, senha, jornada,
// app...) — data minimization é também um controle de segurança. O email é
// mantido por ser o `cod_relator` exigido pelo SAC (abrir/listar chamados).
func Projetar(doc UsuarioDoc) Perfil {
	p := Perfil{Encontrado: true, Login: doc.Login, Nome: doc.Nome, Email: doc.Email}

	for grupoNome, grupo := range doc.Grupos {
		if !grupo.Ativo {
			continue // membership inativa: fora do escopo
		}
		emp := Empresa{
			GrupoEmpresa: grupoNome,
			GrupoUser:    grupo.GrupoUser,
			Area:         grupo.Area,
		}
		for baseNome, base := range grupo.Bases {
			var mods []string
			for modNome, mod := range base.Modulos {
				if mod.Ativo {
					mods = append(mods, modNome)
				}
			}
			sort.Strings(mods)
			emp.Bases = append(emp.Bases, Base{
				Nome:          firstNonEmpty(base.Nome, baseNome),
				Sigla:         base.Sigla,
				ModulosAtivos: mods,
			})
		}
		sort.Slice(emp.Bases, func(i, j int) bool { return emp.Bases[i].Nome < emp.Bases[j].Nome })
		p.Empresas = append(p.Empresas, emp)
	}
	sort.Slice(p.Empresas, func(i, j int) bool { return p.Empresas[i].GrupoEmpresa < p.Empresas[j].GrupoEmpresa })
	return p
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
