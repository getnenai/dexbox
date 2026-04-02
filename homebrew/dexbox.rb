class Dexbox < Formula
  desc "Computer-use tool server for Windows VMs and RDP desktops"
  homepage "https://github.com/getnenai/dexbox"
  version "1.0.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/getnenai/dexbox/releases/download/v#{version}/dexbox_darwin_arm64.tar.gz"
      sha256 "PLACEHOLDER"
    end
    on_intel do
      url "https://github.com/getnenai/dexbox/releases/download/v#{version}/dexbox_darwin_amd64.tar.gz"
      sha256 "PLACEHOLDER"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/getnenai/dexbox/releases/download/v#{version}/dexbox_linux_arm64.tar.gz"
      sha256 "PLACEHOLDER"
    end
    on_intel do
      url "https://github.com/getnenai/dexbox/releases/download/v#{version}/dexbox_linux_amd64.tar.gz"
      sha256 "PLACEHOLDER"
    end
  end

  def install
    bin.install "dexbox"
  end

  def caveats
    <<~EOS
      RDP desktop support requires Docker (for guacd).
      VirtualBox VMs work without Docker.

      Get started:
        dexbox start
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/dexbox --version")
  end
end
