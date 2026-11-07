#!/usr/bin/env python3
"""Build Stream DVR QPKG in QDK self-extracting format.

Same format as the reference package (works on TS-464 / QTS 5.2.10):
    [#!/bin/sh wrapper][control.tar member][zero pad][data.tar.gz][100-byte tail]
The wrapper dd-skips `script_len` bytes to the control tar member; data starts at
script_len + 20480. QTS validates the tail flag "QNAPQPKG  " and the checksum
field at tail[40:50] = first 10 digits of (file_size*3589 + 1000000000).
"""
import gzip
import io
import os
import shutil
import sys
import tarfile

ROOT = os.path.dirname(os.path.abspath(__file__))
WORK = os.path.join(ROOT, "payload")
CONTROL_DIR = os.path.join(ROOT, "control")
OUT_QPKG = os.path.join(ROOT, "..", "..", "build", "StreamDVR_3.0.0_x86_64.qpkg")

NAME = "StreamDVR"
DISPLAY_NAME = "Stream DVR"
VER = "3.0.0"
PAD = 20480  # QDK reserves this many bytes for the control tar member
BUILD_DATE = __import__("datetime").date.today().strftime("%Y%m%d")


def make_data_tar():
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.GNU_FORMAT) as tar:
        for entry in sorted(os.listdir(WORK)):
            if entry == ".DS_Store":
                continue
            tar.add(os.path.join(WORK, entry), arcname=entry, recursive=True)
    return buf.getvalue()


def make_control_tar_gz(tmp):
    control_dir = os.path.join(tmp, "control")
    os.makedirs(control_dir)
    for f in ("qpkg.cfg", "qinstall.sh", "package_routines"):
        shutil.copy2(os.path.join(CONTROL_DIR, f), control_dir)
    with open(os.path.join(control_dir, "built_info"), "w") as f:
        f.write(f"time = {BUILD_DATE}\n")
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.GNU_FORMAT) as tar:
        for entry in sorted(os.listdir(control_dir)):
            tar.add(os.path.join(control_dir, entry), arcname=entry, recursive=False)
    raw = buf.getvalue()
    return raw, gzip.compress(raw, mtime=0)


def control_tar_member(control_gz):
    member = tarfile.TarInfo("control.tar.gz")
    member.size = len(control_gz)
    member.mtime = 0
    member.mode = 0o644
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w", format=tarfile.GNU_FORMAT) as tar:
        tar.addfile(member, io.BytesIO(control_gz))
    return buf.getvalue()


WRAPPER = """#!/bin/sh
find_base(){
HDD_MOUNT=`/sbin/getcfg Public path -f /etc/config/smb.conf`
local log_tool="/sbin/log_tool -t2 -uSystem -p127.0.0.1 -mlocalhost -a"

if [ -e "$HDD_MOUNT" ]; then
if [ -z "$QINSTALL_PATH" ]; then
BASE_GROUP="/share/HDA_DATA /share/HDB_DATA /share/HDC_DATA /share/HDD_DATA /share/HDE_DATA /share/HDF_DATA /share/HDG_DATA /share/HDH_DATA /share/HDI_DATA /share/HDJ_DATA /share/HDK_DATA /share/HDL_DATA /share/MD0_DATA /share/MD1_DATA /share/MD2_DATA /share/MD3_DATA"
publicdir=`/sbin/getcfg Public path -f /etc/config/smb.conf`
if [ ! -z $publicdir ] && [ -d $publicdir ];then
publicdirp1=`/bin/echo $publicdir | /bin/cut -d "/" -f 2`
publicdirp2=`/bin/echo $publicdir | /bin/cut -d "/" -f 3`
publicdirp3=`/bin/echo $publicdir | /bin/cut -d "/" -f 4`
if [ ! -z $publicdirp1 ] && [ ! -z $publicdirp2 ] && [ ! -z $publicdirp3 ]; then
[ -d "/${publicdirp1}/${publicdirp2}/Public" ] && QPKG_BASE="/${publicdirp1}/${publicdirp2}"
fi
fi

if [ -z $QPKG_BASE ]; then
for datadirtest in $BASE_GROUP; do
[ -d $datadirtest/Public ] && QPKG_BASE="/${publicdirp1}/${publicdirp2}"
done
fi
if [ -z $QPKG_BASE ] ; then
echo "The Public share not found."
return 1
fi
QPKG_INSTALL_PATH="${QPKG_BASE}/.qpkg"
QPKG_DIR="${QPKG_INSTALL_PATH}/${QPKG_NAME}"
else
if [ -e "$QINSTALL_PATH" ]; then
QPKG_INSTALL_PATH="${QINSTALL_PATH}"
QPKG_DIR="${QINSTALL_PATH}/${QPKG_NAME}"
else
echo -1 > /tmp/update_process && exit 1
fi
fi
return 0
else
echo -1 > /tmp/update_process && exit 1
fi
}
wrong_arch(){
if [ -x "/usr/local/sbin/notify" ]; then
/sbin/getcfg "" "" -f /tmp/update_process >/dev/null 2>&1
/usr/local/sbin/notify send -A A039 -C C001 -M 51 -l error -t 3 "[{0}] {1} {2} install failed due to the platform is incompatible. Please use correct package for installation." "App Center" "{{DISPLAY}}" "{{VER}}"
fi
echo -1 > /tmp/update_process && exit 1
}
arch_ok(){
local cpu_arch=$(/bin/uname -m)
[ $(/usr/bin/expr match "$cpu_arch" "x86_64") -ne 0 ] || return 1
return 0
}
/bin/echo "Install QNAP package on TS-NAS..."
/bin/grep "/mnt/HDA_ROOT" /proc/mounts >/dev/null 2>&1 || exit 1
arch_ok || wrong_arch
find_base
_EXTRACT_DIR="$QPKG_INSTALL_PATH/.tmp"
/bin/mkdir -p $_EXTRACT_DIR || exit 1
script_len={{SCRIPT_LEN}}
/bin/dd if="${0}" bs=$script_len skip=1 | /bin/tar -xO | /bin/tar -xzv -C $_EXTRACT_DIR || exit 1
offset=$(/usr/bin/expr $script_len + 20480)
/bin/dd if="${0}" bs=$offset skip=1 | /bin/cat | /bin/dd bs=1024 count={{DATA_COUNT}} of=$_EXTRACT_DIR/data.tar.gz || exit 1
( cd $_EXTRACT_DIR && /bin/sh qinstall.sh || echo "Installation Abort." )
/bin/rm -fr $_EXTRACT_DIR && exit 10
exit 1
"""


def build_wrapper(data_count):
    script_len_guess = None
    for _ in range(10):
        val = script_len_guess if script_len_guess is not None else 4353
        text = WRAPPER.replace("{{DISPLAY}}", DISPLAY_NAME).replace("{{VER}}", VER)
        text = text.replace("{{PAD}}", str(PAD)).replace("{{SCRIPT_LEN}}", str(val))
        text = text.replace("{{DATA_COUNT}}", str(data_count))
        enc = text.encode("utf-8")
        if script_len_guess == len(enc):
            return enc
        script_len_guess = len(enc)
    raise RuntimeError("script_len did not converge")


def make_tail():
    model = " ".rjust(10)
    reserved_fw = " ".rjust(50)
    name = NAME.ljust(20)[:20]
    ver = VER.ljust(10)[:10]
    flag = "QNAPQPKG  "
    tail = f"{model}{reserved_fw}{name}{ver}{flag}".encode("ascii")
    assert len(tail) == 100, len(tail)
    return tail


def add_tail_checksum(qpkg):
    size = len(qpkg)
    en = str(size * 3589 + 1000000000)[:10].encode("ascii")
    head, tail = qpkg[:-100], qpkg[-100:]
    tail = tail[:40] + en + tail[50:]
    return head + tail


def main():
    tmp = "/tmp/chatur_bate_dvr_qpkg_build"
    shutil.rmtree(tmp, ignore_errors=True)
    os.makedirs(tmp)

    data_tar_raw = make_data_tar()
    data_gz = gzip.compress(data_tar_raw, mtime=0)
    data_count = (len(data_gz) + 1023) // 1024
    data_blob = data_gz + b"\x00" * (data_count * 1024 - len(data_gz))
    print(f"data.tar.gz: {len(data_gz)} bytes -> {data_count} x 1024B blocks")

    _, control_gz = make_control_tar_gz(tmp)
    control_tar = control_tar_member(control_gz)
    if len(control_tar) > PAD:
        raise RuntimeError(f"control tar {len(control_tar)} exceeds reserved {PAD} bytes")
    print(f"control.tar: {len(control_tar)} bytes")

    wrapper = build_wrapper(data_count)
    script_len = len(wrapper)
    offset = script_len + PAD
    qpkg = wrapper + control_tar + b"\x00" * (offset - script_len - len(control_tar)) + data_blob
    qpkg = add_tail_checksum(qpkg + make_tail())
    with open(OUT_QPKG, "wb") as f:
        f.write(qpkg)
    print(f"written {OUT_QPKG}: {len(qpkg)} bytes (script_len={script_len}, data_offset={offset})")

    head = open(OUT_QPKG, "rb").read(script_len)
    assert head.startswith(b"#!/bin/sh"), "wrapper does not start with #!/bin/sh"
    assert str(script_len).encode() in head
    assert qpkg[-10:] == b"QNAPQPKG  ", "QPKG tail flag missing"
    en = str(len(qpkg) * 3589 + 1000000000)[:10].encode()
    assert qpkg[-100:][40:50] == en, "tail checksum mismatch"
    shutil.rmtree(tmp, ignore_errors=True)
    print("build OK")


if __name__ == "__main__":
    main()