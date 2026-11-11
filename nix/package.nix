{
  lib,
  stdenv,
  buildGoModule,
  makeWrapper,
  chromium,
  tesseract,
  src,
  version,
}:

buildGoModule {
  pname = "nbdl";
  inherit src version;

  vendorHash = "sha256-h4U43W3hLoF+p25/jNRaW8okeEzAZQEmKtwB5l4kGW4=";
  subPackages = [ "cmd/nbdl" ];

  nativeBuildInputs = [ makeWrapper ];

  # Native Tesseract reads text faster than nbdl's built-in tesseract.js.
  # Chromium downloaded by go-rod cannot run on NixOS, so point nbdl at the
  # packaged one. nixpkgs has no Chromium for macOS; there nbdl finds an
  # installed Chrome or downloads Chromium itself.
  postInstall =
    let
      ocr = tesseract.override {
        enableLanguages = [
          "nor"
          "eng"
        ];
      };
    in
    ''
      wrapProgram $out/bin/nbdl --prefix PATH : ${ocr}/bin \
        ${lib.optionalString stdenv.isLinux "--set-default NBDL_BROWSER ${lib.getExe chromium}"}
    '';

  meta = {
    description = "Download books from the Norwegian National Library (nb.no) as PDF";
    homepage = "https://github.com/alcxyz/NB.no-Downloader";
    license = lib.licenses.mit;
    mainProgram = "nbdl";
  };
}
