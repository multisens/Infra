// Swagger UI do edgegateway: um UI, DUAS specs no dropdown — as duas
// superficies diferem por norma (o erro 106 decorre dessa distincao).
// Specs geradas no build da tabela unica (M4) e servidas da mesma origem.
window.onload = function () {
  window.ui = SwaggerUIBundle({
    urls: [
      { url: "specs/openapi-external.json", name: "TV3 WS — superficie externa (44643)" },
      { url: "specs/openapi-internal.json", name: "TV3 WS — superficie interna (44642)" },
    ],
    "urls.primaryName": "TV3 WS — superficie externa (44643)",
    dom_id: "#swagger-ui",
    deepLinking: true,
    presets: [SwaggerUIBundle.presets.apis, SwaggerUIStandalonePlugin],
    layout: "StandaloneLayout",
  });
};
