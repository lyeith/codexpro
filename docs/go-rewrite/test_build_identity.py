import copy
import tempfile
import unittest
from pathlib import Path
from unittest import mock
import build_identity as b


def process(pid, parent=0, executable='/usr/bin/timeout', birth=100):
    return {'pid':pid,'process_start_ticks':birth,'state':'S','ppid':parent,
        'process_group':10,'session':10,'cpu_ticks':1,'nice':10,
        'executable':executable,'executable_device':1,'executable_inode':pid,'executable_bytes':100,'cgroup':'/own'}


class IdentityGuards(unittest.TestCase):
    def test_descendant_compiler_is_observed_without_any_signal(self):
        rows={10:process(10),11:process(11,10,'/tool/bin/go'),12:process(12,11,'/tool/pkg/tool/linux_amd64/compile')}
        with mock.patch.object(b,'process_snapshot',side_effect=lambda pid:copy.deepcopy(rows.get(pid))), \
             mock.patch.object(b,'children_of',side_effect=lambda pid:{10:{11},11:{12}}.get(pid,set())), \
             mock.patch.object(b,'group_limits',return_value={'cpu.weight':'100'}), \
             mock.patch.object(b,'hash_executable',return_value={'sha256':'a'*64}):
            observed=b.BuildIdentity(10)
            rows[12]['cpu_ticks']=20
            observed.observe(force=True)
            result=observed.finish()
            self.assertTrue(result['actual_go_compiler_observed'])
            self.assertEqual(result['observed_compile_processes'],1)
            compiler=next(row for row in result['processes'] if row['pid']==12)
            self.assertEqual(compiler['observed_cpu_tick_increase'],19)
            self.assertEqual(result['identity_errors'],[])

    def test_reused_child_with_other_parent_is_not_attributed(self):
        rows={10:process(10),12:process(12,999,'/tool/compile')}
        with mock.patch.object(b,'process_snapshot',side_effect=lambda pid:copy.deepcopy(rows.get(pid))), \
             mock.patch.object(b,'children_of',side_effect=lambda pid:{12} if pid==10 else set()), \
             mock.patch.object(b,'group_limits',return_value={}), \
             mock.patch.object(b,'hash_executable',return_value={}):
            result=b.BuildIdentity(10).finish()
            self.assertFalse(result['actual_go_compiler_observed'])
            self.assertEqual([row['pid'] for row in result['processes']],[10])

    def test_leader_reuse_is_explicit_and_does_not_collect_new_tree(self):
        rows={10:process(10)}
        with mock.patch.object(b,'process_snapshot',side_effect=lambda pid:copy.deepcopy(rows.get(pid))), \
             mock.patch.object(b,'children_of',return_value=set()) as children, \
             mock.patch.object(b,'group_limits',return_value={}), \
             mock.patch.object(b,'hash_executable',return_value={}):
            observer=b.BuildIdentity(10); children.reset_mock()
            rows[10]['process_start_ticks']=200
            observer.observe(force=True)
            children.assert_not_called()
            self.assertIn('load leader incarnation changed',observer.finish()['identity_errors'])

    def test_fast_exit_does_not_bypass_existing_wrapper_cleanup(self):
        with mock.patch.object(b,'process_snapshot',return_value=None):
            observer=b.BuildIdentity(10)
            result=observer.finish()
            self.assertFalse(result['actual_go_compiler_observed'])
            self.assertTrue(result['identity_errors'])

    def test_changed_binary_is_not_hashed_as_the_observed_executable(self):
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory)/'compile';path.write_bytes(b'new')
            record={'executable':str(path),'executable_device':0,'executable_inode':0,'executable_bytes':3}
            result=b.hash_executable(record)
            self.assertIsNone(result['sha256'])
            self.assertIn('identity changed',result['reason'])


if __name__=='__main__':unittest.main()
