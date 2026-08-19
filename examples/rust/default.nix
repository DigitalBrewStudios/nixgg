# Rust example — demonstrates nixgg's ability to accelerate Rust builds.
# Uses Cargo as the build system, which is a common pattern for Rust projects.
#
# This example shows how to integrate nixgg with Rust projects that use
# Cargo for building, while still getting per-TU acceleration for the
# compilation steps.
{
  mkNixggBuild,
  src,
  rustc,
  cargo,
  pkg-config,
}:


mkNixggBuild {
  pname = "rust-example";
  version = "0.1.0";
  inherit src;
  
  # Rust artifacts are typically built into a target directory
  # The target name is the final binary that gets produced
  target = "rust-example";
  
  # Need Cargo and Rust compiler for the build
  nativeBuildInputs = [ rustc cargo pkg-config ];
  
  buildCommand = ''
    # Create a minimal Cargo project structure
    mkdir -p src
    cat > src/main.rs <<'EOF'
    fn main() {
        println!("Hello from nixgg-accelerated Rust!");
    }
    EOF
    
    # Build using Cargo
    cargo build --release
  '';
}