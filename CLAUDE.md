# Bot de agendamiento

## Centro de conocimiento

Esta carpeta es la base de conocimiento del proyecto. Al empezar una tarea, leé
`README.md` (índice) y después los documentos que apliquen. El código vive en `app/` y
su documentación técnica en `app/docs/`.

- **Raíz** (`plan.md`, `plan_tecnico.md`, nuevos `.md`): negocio, roadmap, costos,
  riesgos, decisiones, investigación de proveedores.
- **`app/docs/`**: cómo funciona el sistema (arquitectura, flujos, datos, integraciones,
  trampas).

## Cuándo y cómo documentar

Cuando implementes algo o aprendas algo no obvio y durable, actualizá el documento del
tema en el mismo cambio:

1. **Un documento por tema.** Si ya existe, complementalo en vez de crear uno nuevo.
2. **Citá `archivo.go:línea`** para lo implementado. Al implementar algo que estaba en
   diseño, reemplazá la descripción por la referencia y cambiá el estado del documento.
3. **Marcá lo no verificado con [verificar].** Si confirmás algo, sacá la marca y anotá
   la fuente.
4. **Actualizá el índice** (`README.md` de la raíz o de `app/docs/`).
5. **Fechá los cambios** en la línea de estado del documento.
6. Español.

No documentes lo que el código o git ya dicen por sí solos, ni el estado efímero de una
tarea.
