# Until the first release is published, this formula builds Sheaf from the
# main branch: brew install --HEAD sensahin/sheaf/sheaf
# Each stable release replaces it with one that installs the release binaries
# (see scripts/homebrew-formula.sh).
class Sheaf < Formula
  desc "Local-first coordination layer for AI coding agents"
  homepage "https://github.com/sensahin/sheaf"
  license "Apache-2.0"
  head "https://github.com/sensahin/sheaf.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X main.version=#{version}"), "./cmd/sheaf"
  end

  service do
    run [opt_bin/"sheaf", "daemon"]
    keep_alive true
    log_path var/"log/sheaf.log"
    error_log_path var/"log/sheaf.log"
  end

  test do
    assert_match "sheaf", shell_output("#{bin}/sheaf version")
  end
end
